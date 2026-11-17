package replayer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// CheckNames are the checks of every verdict, in report order.
var CheckNames = []string{
	"exactly_once_mirror",
	"exactly_once_source",
	"no_deleted",
	"mirror_last_seq",
	"c1_flow_control_active",
	"c2_not_recreated",
	"c3_duplicate_refused",
	"c4_stored_unchanged",
}

// Sequences are the per-stream sequence lists a verdict reports.
type Sequences struct {
	Missing        []uint64 `json:"missing"`
	Duplicates     []uint64 `json:"duplicates"`
	DigestMismatch []uint64 `json:"digest_mismatch"`
	Extra          []uint64 `json:"extra"`
	Deleted        []uint64 `json:"deleted"`
	LastSeq        uint64   `json:"last_seq"`
}

// Verdict is the content of verdict.json.
type Verdict struct {
	Timeline      string                    `json:"timeline"`
	Nodes         uint64                    `json:"nodes"`
	Seed          uint64                    `json:"seed"`
	Pass          bool                      `json:"pass"`
	Checks        map[string]bool           `json:"checks"`
	Reasons       map[string]map[string]any `json:"reasons"`
	Preconditions map[string]bool           `json:"preconditions"`
	TimelineError string                    `json:"timeline_error"`
	OriginLastSeq uint64                    `json:"origin_last_seq"`
	Boundary      string                    `json:"boundary"`
	Mirror        Sequences                 `json:"mirror"`
	Source        Sequences                 `json:"source"`
}

// LoadRecording reads recording.jsonl.
func LoadRecording(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			return nil, fmt.Errorf("recording line: %w", err)
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}

// WriteJSON writes v as canonical JSON: sorted keys, compact, one trailing newline.
func WriteJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		if e, ok := v.(Event); ok {
			return e
		}
	}
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asUint(v any) uint64 {
	switch n := v.(type) {
	case json.Number:
		var u uint64
		if _, err := fmt.Sscan(n.String(), &u); err == nil {
			return u
		}
	case float64:
		return uint64(n)
	case int64:
		return uint64(n)
	case uint64:
		return n
	case int:
		return uint64(n)
	}
	return 0
}

type storedView struct {
	ok       bool
	created  string
	lastSeq  uint64
	deleted  []uint64
	messages uint64
	bySeq    map[uint64]string // downstream seq -> digest
	origin   map[uint64]uint64 // downstream seq -> origin seq (0 when unknown)
	seqs     []uint64
}

func viewOf(snap map[string]any, stream string) storedView {
	s := asMap(asMap(snap["streams"])[stream])
	v := storedView{bySeq: map[uint64]string{}, origin: map[uint64]uint64{}}
	if s == nil || !asBool(s["ok"]) {
		return v
	}
	v.ok = true
	v.created = asString(s["created"])
	v.lastSeq = asUint(s["last_seq"])
	v.messages = asUint(s["messages"])
	for _, d := range asList(s["deleted"]) {
		v.deleted = append(v.deleted, asUint(d))
	}
	for _, raw := range asList(s["msgs"]) {
		m := asMap(raw)
		seq := asUint(m["seq"])
		v.bySeq[seq] = asString(m["digest"])
		v.origin[seq] = asUint(m["origin_seq"])
		v.seqs = append(v.seqs, seq)
	}
	slices.Sort(v.seqs)
	return v
}

func sortedKeys(m map[uint64]bool) []uint64 {
	out := make([]uint64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func nonNil(l []uint64) []uint64 {
	if l == nil {
		return []uint64{}
	}
	return l
}

// exactlyOnce compares a downstream view against the origin view.
func exactlyOnce(origin, down storedView, mirror bool) (Sequences, bool) {
	seqs := Sequences{LastSeq: down.lastSeq, Deleted: nonNil(slices.Clone(down.deleted))}
	counts := map[uint64]int{}
	var maxOrigin uint64
	for _, dseq := range down.seqs {
		oseq := down.origin[dseq]
		odigest, known := origin.bySeq[oseq]
		if oseq == 0 || !known {
			seqs.Extra = append(seqs.Extra, dseq)
			continue
		}
		counts[oseq]++
		maxOrigin = max(maxOrigin, oseq)
		if odigest != down.bySeq[dseq] {
			seqs.DigestMismatch = append(seqs.DigestMismatch, oseq)
		}
	}
	for _, oseq := range origin.seqs {
		switch c := counts[oseq]; {
		case c == 0:
			seqs.Missing = append(seqs.Missing, oseq)
		case c > 1:
			seqs.Duplicates = append(seqs.Duplicates, oseq)
		}
	}
	seqs.Missing, seqs.Duplicates = nonNil(seqs.Missing), nonNil(seqs.Duplicates)
	seqs.DigestMismatch, seqs.Extra = nonNil(seqs.DigestMismatch), nonNil(seqs.Extra)
	last := maxOrigin
	if mirror {
		last = down.lastSeq
	}
	ok := len(seqs.Missing) == 0 && len(seqs.Duplicates) == 0 && len(seqs.DigestMismatch) == 0 &&
		len(seqs.Extra) == 0 && last == origin.lastSeq
	return seqs, ok
}

// Judge derives the verdict from a recording alone.
func Judge(events []Event) *Verdict {
	v := &Verdict{
		Checks:        map[string]bool{},
		Reasons:       map[string]map[string]any{},
		Preconditions: map[string]bool{},
	}
	var snaps []map[string]any
	snapByBoundary := map[string]map[string]any{}
	var dup map[string]any
	var advisories []map[string]any
	flowControlMsgs := 0
	for _, ev := range events {
		switch asString(ev["type"]) {
		case "run":
			v.Timeline = asString(ev["timeline"])
			v.Nodes = asUint(ev["nodes"])
			v.Seed = asUint(ev["seed"])
		case "snapshot":
			snaps = append(snaps, ev)
			snapByBoundary[asString(ev["boundary"])] = ev
		case "duplicate":
			dup = ev
		case "advisory":
			advisories = append(advisories, ev)
		case "control":
			if asString(ev["kind"]) == "flow_control" {
				flowControlMsgs++
			}
		case "precondition":
			v.Preconditions[asString(ev["name"])] = asBool(ev["ok"])
		case "timeline_error":
			v.TimelineError = asString(ev["error"])
		}
	}
	for _, name := range CheckNames {
		v.Checks[name] = false
	}
	fail := func(check, code string, detail map[string]any) {
		v.Checks[check] = false
		r := map[string]any{"code": code}
		for k, val := range detail {
			r[k] = val
		}
		v.Reasons[check] = r
	}
	var final map[string]any
	if len(snaps) > 0 {
		final = snaps[len(snaps)-1]
		v.Boundary = asString(final["boundary"])
	}
	first := snapByBoundary["after_batch1"]
	if final == nil || first == nil {
		for _, name := range CheckNames {
			fail(name, "snapshot_unavailable", nil)
		}
		v.Mirror, v.Source = emptySequences(), emptySequences()
		return finish(v)
	}
	origin := viewOf(final, OriginStream)
	mirror := viewOf(final, MirrorStream)
	source := viewOf(final, SourceStream)
	v.OriginLastSeq = origin.lastSeq

	// exactly_once_mirror, exactly_once_source.
	for _, d := range []struct {
		check  string
		view   storedView
		mirror bool
		out    *Sequences
	}{
		{"exactly_once_mirror", mirror, true, &v.Mirror},
		{"exactly_once_source", source, false, &v.Source},
	} {
		if !origin.ok || !d.view.ok {
			*d.out = emptySequences()
			fail(d.check, "snapshot_unavailable", nil)
			continue
		}
		seqs, ok := exactlyOnce(origin, d.view, d.mirror)
		*d.out = seqs
		if ok {
			v.Checks[d.check] = true
			continue
		}
		fail(d.check, "not_exactly_once", map[string]any{
			"missing": seqs.Missing, "duplicates": seqs.Duplicates, "digest_mismatch": seqs.DigestMismatch,
			"extra": seqs.Extra, "last_seq": seqs.LastSeq, "origin_last_seq": origin.lastSeq,
		})
	}

	// no_deleted: no snapshot of either downstream stream lists a deleted sequence.
	delM, delS := map[uint64]bool{}, map[uint64]bool{}
	for _, s := range snaps {
		for _, d := range viewOf(s, MirrorStream).deleted {
			delM[d] = true
		}
		for _, d := range viewOf(s, SourceStream).deleted {
			delS[d] = true
		}
	}
	if len(delM) == 0 && len(delS) == 0 {
		v.Checks["no_deleted"] = true
	} else {
		fail("no_deleted", "deleted", map[string]any{"mirror": sortedKeys(delM), "source": sortedKeys(delS)})
	}

	// mirror_last_seq: at no boundary does the mirror hold a sequence while an
	// earlier one the origin holds is missing from it.
	gaps := map[uint64]bool{}
	var gapBoundaries []string
	for _, s := range snaps {
		o, m := viewOf(s, OriginStream), viewOf(s, MirrorStream)
		if !o.ok || !m.ok {
			continue
		}
		found := false
		for _, oseq := range o.seqs {
			if oseq >= m.lastSeq {
				break
			}
			if _, held := m.bySeq[oseq]; !held {
				gaps[oseq], found = true, true
			}
		}
		if found {
			gapBoundaries = append(gapBoundaries, asString(s["boundary"]))
		}
	}
	if len(gaps) == 0 {
		v.Checks["mirror_last_seq"] = true
	} else {
		fail("mirror_last_seq", "later_seq_with_earlier_missing",
			map[string]any{"missing": sortedKeys(gaps), "boundaries": gapBoundaries})
	}

	// c1_flow_control_active.
	fc := asBool(asMap(final["consumer"])["flow_control"])
	if fc && flowControlMsgs > 0 {
		v.Checks["c1_flow_control_active"] = true
	} else {
		fail("c1_flow_control_active", "flow_control_inactive",
			map[string]any{"config_flow_control": fc, "flow_control_messages": flowControlMsgs})
	}

	// c2_not_recreated.
	var changed []string
	if a, b := asMap(first["consumer"]), asMap(final["consumer"]); asString(a["created"]) == "" ||
		asString(a["created"]) != asString(b["created"]) {
		changed = append(changed, "consumer:"+Consumer)
	}
	for _, name := range []string{MirrorStream, SourceStream} {
		a, b := viewOf(first, name), viewOf(final, name)
		if a.created == "" || a.created != b.created {
			changed = append(changed, "stream:"+name)
		}
	}
	watched := map[string]bool{}
	for _, kind := range []string{"CREATED", "DELETED"} {
		watched["$JS.EVENT.ADVISORY.CONSUMER."+kind+"."+OriginStream+"."+Consumer] = true
		watched["$JS.EVENT.ADVISORY.STREAM."+kind+"."+MirrorStream] = true
		watched["$JS.EVENT.ADVISORY.STREAM."+kind+"."+SourceStream] = true
	}
	firstID := asUint(first["id"])
	var lifecycle []string
	for _, a := range advisories {
		if asUint(a["id"]) > firstID && watched[asString(a["subject"])] {
			lifecycle = append(lifecycle, asString(a["subject"]))
		}
	}
	if len(changed) == 0 && len(lifecycle) == 0 {
		v.Checks["c2_not_recreated"] = true
	} else {
		fail("c2_not_recreated", "recreated", map[string]any{
			"changed": nonNilStrings(changed), "advisories": nonNilStrings(lifecycle),
		})
	}

	// c3_duplicate_refused: the origin refused the second copy of a message
	// published twice under one id, and each downstream holds exactly one copy.
	if dup == nil {
		fail("c3_duplicate_refused", "no_duplicate", nil)
	} else {
		k := asUint(dup["first_seq"])
		refused := asBool(dup["second_duplicate"])
		copies := map[string]uint64{}
		ok := refused && k > 0
		for _, name := range []string{MirrorStream, SourceStream} {
			f := viewOf(final, name)
			for _, dseq := range f.seqs {
				if f.origin[dseq] == k {
					copies[name]++
				}
			}
			if !f.ok || copies[name] != 1 {
				ok = false
			}
		}
		if ok {
			v.Checks["c3_duplicate_refused"] = true
		} else {
			fail("c3_duplicate_refused", "duplicate_accepted", map[string]any{
				"origin_seq": k, "second_duplicate": refused, "final_copies": copies,
			})
		}
	}

	// c4_stored_unchanged.
	lost := map[string][]uint64{}
	allOK := true
	for _, name := range []string{MirrorStream, SourceStream} {
		a, b := viewOf(first, name), viewOf(final, name)
		if !a.ok || !b.ok {
			allOK = false
			continue
		}
		for _, seq := range a.seqs {
			if b.bySeq[seq] != a.bySeq[seq] {
				lost[name] = append(lost[name], seq)
			}
		}
	}
	if allOK && len(lost) == 0 {
		v.Checks["c4_stored_unchanged"] = true
	} else {
		fail("c4_stored_unchanged", "stored_changed", map[string]any{
			"mirror": nonNil(lost[MirrorStream]), "source": nonNil(lost[SourceStream]),
			"snapshots_ok": allOK,
		})
	}
	return finish(v)
}

func emptySequences() Sequences {
	return Sequences{
		Missing: []uint64{}, Duplicates: []uint64{}, DigestMismatch: []uint64{}, Extra: []uint64{},
		Deleted: []uint64{},
	}
}

func nonNilStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func finish(v *Verdict) *Verdict {
	pass := v.TimelineError == ""
	for _, name := range CheckNames {
		pass = pass && v.Checks[name]
	}
	var failed []string
	for name, ok := range v.Preconditions {
		if !ok {
			failed = append(failed, name)
		}
	}
	slices.Sort(failed)
	if len(failed) > 0 {
		pass = false
		v.Reasons["preconditions"] = map[string]any{"code": "precondition_failed", "failed": failed}
	}
	if v.TimelineError != "" {
		v.Reasons["timeline"] = map[string]any{"code": "timeline_error", "error": strings.TrimSpace(v.TimelineError)}
	}
	v.Pass = pass
	return v
}
