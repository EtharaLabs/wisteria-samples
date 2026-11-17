package replayer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nats-io/nats.go"

	"replay.local/workbench/controller"
)

type clusterInfo struct {
	Leader string `json:"leader"`
}

type sourceInfo struct {
	Name  string               `json:"name"`
	Lag   uint64               `json:"lag"`
	Error *controller.APIError `json:"error"`
}

type streamInfo struct {
	Created string `json:"created"`
	State   struct {
		Msgs       uint64   `json:"messages"`
		FirstSeq   uint64   `json:"first_seq"`
		LastSeq    uint64   `json:"last_seq"`
		NumDeleted int      `json:"num_deleted"`
		Deleted    []uint64 `json:"deleted"`
	} `json:"state"`
	Cluster *clusterInfo  `json:"cluster"`
	Mirror  *sourceInfo   `json:"mirror"`
	Sources []*sourceInfo `json:"sources"`
}

type seqInfo struct {
	Consumer uint64 `json:"consumer_seq"`
	Stream   uint64 `json:"stream_seq"`
}

type consumerInfo struct {
	Created string `json:"created"`
	Config  *struct {
		FlowControl bool `json:"flow_control"`
	} `json:"config"`
	Delivered     seqInfo      `json:"delivered"`
	AckFloor      seqInfo      `json:"ack_floor"`
	NumAckPending int          `json:"num_ack_pending"`
	NumPending    uint64       `json:"num_pending"`
	Cluster       *clusterInfo `json:"cluster"`
}

type storedMsg struct {
	Message *struct {
		Subject string `json:"subject"`
		Seq     uint64 `json:"seq"`
		Header  []byte `json:"hdrs"`
		Data    []byte `json:"data"`
	} `json:"message"`
}

type pubAck struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Duplicate bool   `json:"duplicate"`
}

func getStreamInfo(ctx context.Context, nc *nats.Conn, name string) (*streamInfo, error) {
	var si streamInfo
	_, err := controller.Request(ctx, nc, "$JS.API.STREAM.INFO."+name, map[string]bool{"deleted_details": true}, &si)
	if err != nil {
		return nil, err
	}
	return &si, nil
}

func getConsumerInfo(ctx context.Context, nc *nats.Conn, stream, name string) (*consumerInfo, error) {
	var ci consumerInfo
	_, err := controller.Request(ctx, nc, fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", stream, name), nil, &ci)
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// errNoMessage is returned for a sequence the stream does not hold.
var errNoMessage = errors.New("no message")

func getMsg(ctx context.Context, nc *nats.Conn, stream string, seq uint64) (*storedMsg, error) {
	var sm storedMsg
	_, err := controller.Request(ctx, nc, "$JS.API.STREAM.MSG.GET."+stream, map[string]uint64{"seq": seq}, &sm)
	if err != nil {
		var apiErr *controller.APIError
		if errors.As(err, &apiErr) && apiErr.Code == 404 {
			return nil, errNoMessage
		}
		return nil, err
	}
	if sm.Message == nil {
		return nil, errNoMessage
	}
	return &sm, nil
}

// Digest is the payload digest the recording uses: the first 16 hex digits of
// the SHA-256 of the payload.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// ackInfo is the pair of sequences a downstream stream reads from a
// delivery's reply subject.
type ackInfo struct {
	Stream    string
	Consumer  string
	Delivered uint64
	StreamSeq uint64
	DelivSeq  uint64
	Pending   uint64
	OK        bool
}

// parseAckReply parses $JS.ACK.<stream>.<consumer>.<dc>.<sseq>.<dseq>.<ts>.<pending>
// and the longer form with domain and account hash prefixed.
func parseAckReply(reply string) ackInfo {
	tokens := strings.Split(reply, ".")
	if len(tokens) < 9 || tokens[0] != "$JS" || tokens[1] != "ACK" {
		return ackInfo{}
	}
	// The short reply is $JS.ACK.<stream>.<consumer>.<deliveries>.<sseq>.<dseq>.
	// <timestamp>.<pending>, nine tokens. The form every current server emits
	// inserts a domain and an account hash after the prefix, which is eleven.
	// Anything else is a shape this parser does not know, and a wrong offset
	// silently yields sequence zero, so refuse it instead.
	var base int
	switch len(tokens) {
	case 9:
		base = 2
	case 11:
		base = 4
	default:
		return ackInfo{}
	}
	// A field that does not parse means the offsets are wrong, and reporting
	// sequence zero as a fact is worse than reporting that the reply was not
	// understood, so one bad field makes the whole parse unusable.
	ok := true
	num := func(i int) uint64 {
		if base+i >= len(tokens) {
			ok = false
			return 0
		}
		v, err := strconv.ParseUint(tokens[base+i], 10, 64)
		if err != nil {
			ok = false
			return 0
		}
		return v
	}
	info := ackInfo{
		Stream:    tokens[base],
		Consumer:  tokens[base+1],
		Delivered: num(2),
		StreamSeq: num(3),
		DelivSeq:  num(4),
		Pending:   num(6),
	}
	info.OK = ok
	return info
}

// sourceOriginSeq reads the origin sequence from a sourced message's
// Nats-Stream-Source header, whose second field is that sequence.
func sourceOriginSeq(hdr []byte) (uint64, bool) {
	for line := range strings.SplitSeq(string(hdr), "\r\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Nats-Stream-Source") {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) < 2 {
			return 0, false
		}
		seq, err := strconv.ParseUint(fields[1], 10, 64)
		return seq, err == nil
	}
	return 0, false
}
