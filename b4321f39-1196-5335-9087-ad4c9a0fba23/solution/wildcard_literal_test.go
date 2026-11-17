// Regression tests for server/stree, written for a test-generation task (reference solution).
// Licensed under the Apache License, Version 2.0, like the repository it extends.

package stree

import (
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Regression tests for wildcard characters that sit inside a longer token.
//
// Reference semantics: a filter token that is exactly "*" matches any one token, and a
// last filter token that is exactly ">" matches one or more remaining tokens. Every other
// byte is literal, including '*' and '>' inside tokens such as "a*", "*a", "**" or ">b".
// The checks never depend on callback order and always compare full subjects and values.

func wlRefMatch(filter, subject string) bool {
	ft, st := strings.Split(filter, "."), strings.Split(subject, ".")
	for i, f := range ft {
		if f == ">" && i == len(ft)-1 {
			return len(st) > i
		}
		if i >= len(st) {
			return false
		}
		if f != "*" && f != st[i] {
			return false
		}
	}
	return len(ft) == len(st)
}

type wlHit struct {
	subj string
	val  int
}

func wlSort(h []wlHit) []wlHit {
	slices.SortFunc(h, func(a, b wlHit) int { return strings.Compare(a.subj, b.subj) })
	return h
}

func wlWant(set map[string]int, filter string) []wlHit {
	out := []wlHit{}
	for s, v := range set {
		if wlRefMatch(filter, s) {
			out = append(out, wlHit{s, v})
		}
	}
	return wlSort(out)
}

// wlMatch collects Match results and checks every callback carries the stored value pointer.
func wlMatch(t testing.TB, st *SubjectTree[int], filter string) []wlHit {
	t.Helper()
	out := []wlHit{}
	st.Match([]byte(filter), func(subj []byte, v *int) {
		if p, ok := st.Find(subj); !ok || p != v {
			t.Errorf("Match(%q): callback for %q does not carry the stored value", filter, subj)
		}
		out = append(out, wlHit{string(subj), *v})
	})
	return wlSort(out)
}

// wlMatchUntil collects MatchUntil results when the callback never stops it.
func wlMatchUntil(t testing.TB, st *SubjectTree[int], filter string) []wlHit {
	t.Helper()
	out := []wlHit{}
	done := st.MatchUntil([]byte(filter), func(subj []byte, v *int) bool {
		if p, ok := st.Find(subj); !ok || p != v {
			t.Errorf("MatchUntil(%q): callback for %q does not carry the stored value", filter, subj)
		}
		out = append(out, wlHit{string(subj), *v})
		return true
	})
	if !done {
		t.Errorf("MatchUntil(%q) returned false although the callback never stopped it", filter)
	}
	return wlSort(out)
}

func wlCheck(t testing.TB, st *SubjectTree[int], set map[string]int, filter string) {
	t.Helper()
	want := wlWant(set, filter)
	if got := wlMatch(t, st, filter); !slices.Equal(got, want) {
		t.Fatalf("Match(%q) over %v\n got  %v\n want %v", filter, wlKeys(set), got, want)
	}
	if got := wlMatchUntil(t, st, filter); !slices.Equal(got, want) {
		t.Fatalf("MatchUntil(%q) over %v\n got  %v\n want %v", filter, wlKeys(set), got, want)
	}
}

func wlKeys(set map[string]int) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func wlBuild(subjects ...string) (*SubjectTree[int], map[string]int) {
	st, set := NewSubjectTree[int](), map[string]int{}
	for i, s := range subjects {
		st.Insert([]byte(s), i+1)
		set[s] = i + 1
	}
	return st, set
}

// Hand-picked tree shapes, one per place where a literal '*' or '>' could be mistaken for a
// wildcard once the radix tree has consumed the bytes in front of it.
func TestWildcardCharInsideTokenShapes(t *testing.T) {
	for _, tc := range []struct {
		subjects []string
		filters  []string
	}{
		// Node split right before the literal '*' / '>': trimmed part reaches child nodes.
		{[]string{"KV.a", "KV.apple", "KV.avocado", "KV.banana"},
			[]string{"KV.a*", "KV.a>", "KV.ap*", "KV.*", "KV.>", "KV.a**", "KV.a*>", "*.a*", "*.a>", ">"}},
		// Literal subjects with wildcard chars inside tokens match exactly.
		{[]string{"KV.a", "KV.apple", "KV.avocado", "KV.banana", "KV.a*", "KV.a*x", "KV.a>", "KV.a>x"},
			[]string{"KV.a*", "KV.a>", "KV.a*x", "KV.a>x", "*.a*", "*.a>", "KV.*", "KV.>", "KV.a*y", ">"}},
		// A trimmed '*' that lands exactly on a node whose prefix is '*'.
		{[]string{"KV.apple", "KV.avocado", "KV.a*x", "KV.a*y"},
			[]string{"KV.a*", "KV.a*x", "KV.a**", "KV.*", "KV.a*.>"}},
		{[]string{"KV.apple", "KV.a*b.c", "KV.a*b.d", "KV.a*c"},
			[]string{"KV.a*", "KV.a*b", "KV.a*b.*", "KV.a*.*", "KV.*.*", "KV.a*b.>"}},
		// A literal token that starts with '>' after a node split.
		{[]string{"x.>b", "x.c", "x.>bc.d"},
			[]string{"x.>bc", "x.>b", "x.>", "x.*", "x.>bc.>", "x.>b*", "*.>b"}},
		{[]string{"foo.>bar", "foo.>baz", "foo.qux"},
			[]string{"foo.>ba", "foo.>bar", "foo.>b", "foo.>", "foo.*"}},
		// Tokens made only of wildcard characters, and wildcard chars at a token start.
		{[]string{"a.**", "a.*b", "a.b*", "a.>>", "a.*>", "a.b", "a.**.c"},
			[]string{"a.**", "a.*b", "a.b*", "a.>>", "a.*>", "a.*", "a.>", "a.**.*", "*.**", "a.*.c"}},
		// Real wildcards must keep working next to literal ones.
		{[]string{"foo.bar.baz", "foo.b*r.baz", "foo.bar.b>z", "foo.b>r.baz.q"},
			[]string{"foo.*.baz", "foo.b*r.*", "*.*.b>z", "foo.>", "foo.b*r.>", "foo.*.*.*", "*.b>r.>"}},
	} {
		st, set := wlBuild(tc.subjects...)
		for _, f := range tc.filters {
			wlCheck(t, st, set, f)
		}
	}
}

// Nodes with more than 48 children (the widest node kind keeps empty slots between them).
func TestWildcardCharInsideTokenWideNodes(t *testing.T) {
	var subjects []string
	for c := byte('!'); c <= '~'; c++ {
		if c == '.' {
			continue
		}
		subjects = append(subjects, "W."+string(c)+"x", "V.q"+string(c), "U.r"+string(c)+".s")
	}
	st, set := wlBuild(subjects...)
	for _, f := range []string{"W.*", "W.>", "V.*", "V.>", "U.*.s", "U.*.*", "*.*", "*.q*", "V.q*", "W.**", "W.*x", "W.>x", "U.r>.*", ">"} {
		wlCheck(t, st, set, f)
	}
}

const wlAlphaNarrow = "ab*>"
const wlAlphaWide = "abcdefghijklmnopqrstuvwxyz0123456789*>"

func wlToken(r *rand.Rand, alpha string) string {
	for {
		n := 1 + r.IntN(3)
		var sb strings.Builder
		for range n {
			sb.WriteByte(alpha[r.IntN(len(alpha))])
		}
		if tok := sb.String(); tok != "*" && tok != ">" {
			return tok
		}
	}
}

func wlSubject(r *rand.Rand, alpha string) string {
	toks := make([]string, 1+r.IntN(4))
	for i := range toks {
		toks[i] = wlToken(r, alpha)
	}
	return strings.Join(toks, ".")
}

func wlValidFilter(f string) bool {
	toks := strings.Split(f, ".")
	for i, tok := range toks {
		if tok == "" || tok == ">" && i != len(toks)-1 {
			return false
		}
	}
	return true
}

// wlFilter usually derives a filter from a stored subject so that matches are likely, mixing
// real wildcards with literal '*' and '>' placed inside tokens.
func wlFilter(r *rand.Rand, subjects []string, alpha string) string {
	for {
		var toks []string
		if len(subjects) > 0 && r.IntN(5) != 0 {
			toks = strings.Split(subjects[r.IntN(len(subjects))], ".")
		} else {
			toks = strings.Split(wlSubject(r, alpha), ".")
		}
		out := make([]string, 0, len(toks)+1)
		for i, tok := range toks {
			switch x := r.IntN(12); {
			case x < 2:
				out = append(out, "*")
			case x == 2:
				out = append(out, ">")
			case x == 3 || x == 4:
				cut := r.IntN(len(tok) + 1)
				out = append(out, tok[:cut]+string("*>"[r.IntN(2)])+tok[cut:])
			case x == 5 && len(tok) > 1:
				out = append(out, tok[:1+r.IntN(len(tok)-1)])
			default:
				out = append(out, tok)
			}
			if out[len(out)-1] == ">" || i == len(toks)-1 && r.IntN(8) == 0 {
				break
			}
		}
		if r.IntN(10) == 0 {
			out = append(out, string("*>"[r.IntN(2)]))
		}
		if f := strings.Join(out, "."); wlValidFilter(f) {
			return f
		}
	}
}

func TestWildcardCharInsideTokenRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(20261030, 8765))
	for round := range 1500 {
		alpha, n := wlAlphaNarrow, 1+r.IntN(10)
		if round%10 == 9 {
			alpha, n = wlAlphaWide, 20+r.IntN(60)
		}
		subjects := make([]string, n)
		for i := range subjects {
			subjects[i] = wlSubject(r, alpha)
		}
		st, set := wlBuild(subjects...)
		for range 8 {
			wlCheck(t, st, set, wlFilter(r, subjects, alpha))
		}
	}
}

// MatchUntil must stop on the first false, report it, and otherwise run to completion.
func TestWildcardCharInsideTokenMatchUntilStops(t *testing.T) {
	for _, tc := range []struct {
		subjects []string
		filter   string
	}{
		{[]string{"KV.apple", "KV.applepie", "KV.apricot", "KV.avocado", "KV.b"}, "KV.*"},
		{[]string{"KV.a", "KV.ab", "KV.ac", "KV.ad.x"}, "KV.*"},
		{[]string{"KV.a", "KV.ab", "KV.ac"}, "KV.a"},
		{[]string{"KV.a*", "KV.a*b", "KV.a*c"}, "KV.a*"},
		{[]string{"x.>a", "x.>ab", "x.>ac", "x.b"}, "x.>a"},
	} {
		st, set := wlBuild(tc.subjects...)
		want := wlWant(set, tc.filter)
		for k := 1; k <= len(want)+1; k++ {
			calls := 0
			done := st.MatchUntil([]byte(tc.filter), func([]byte, *int) bool {
				calls++
				return calls < k
			})
			if wantDone := k > len(want); done != wantDone || calls != min(k, len(want)) {
				t.Fatalf("MatchUntil(%q) over %v stopping at call %d: done=%v calls=%d", tc.filter, tc.subjects, k, done, calls)
			}
		}
	}
	r := rand.New(rand.NewPCG(7, 8765))
	checked := 0
	for checked < 400 {
		subjects := make([]string, 2+r.IntN(10))
		for i := range subjects {
			subjects[i] = wlSubject(r, wlAlphaNarrow)
		}
		st, set := wlBuild(subjects...)
		filter := wlFilter(r, subjects, wlAlphaNarrow)
		want := wlWant(set, filter)
		if len(want) == 0 {
			continue
		}
		checked++
		for k := 1; k <= len(want); k++ {
			calls := 0
			done := st.MatchUntil([]byte(filter), func(subj []byte, v *int) bool {
				calls++
				if !wlRefMatch(filter, string(subj)) || set[string(subj)] != *v {
					t.Errorf("MatchUntil(%q) reported %q=%d", filter, subj, *v)
				}
				return calls < k
			})
			if done || calls != k {
				t.Fatalf("MatchUntil(%q) over %v stopping at call %d: done=%v calls=%d", filter, wlKeys(set), k, done, calls)
			}
		}
	}
}

// Match only reads the tree: concurrent calls on one tree, and on separate trees, must agree
// with the reference. Run with -race.
func TestWildcardCharInsideTokenConcurrentReaders(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 8765))
	subjects := make([]string, 300)
	for i := range subjects {
		subjects[i] = wlSubject(r, wlAlphaNarrow)
	}
	shared, sharedSet := wlBuild(subjects...)
	filters := make([]string, 64)
	for i := range filters {
		filters[i] = wlFilter(r, subjects, wlAlphaNarrow)
	}
	var wg sync.WaitGroup
	for g := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			own, ownSet := wlBuild(subjects[g*40 : g*40+40]...)
			for i := range 3 * len(filters) {
				f := filters[(i+g*7)%len(filters)]
				if got, want := wlMatch(t, shared, f), wlWant(sharedSet, f); !slices.Equal(got, want) {
					t.Errorf("concurrent Match(%q) on shared tree: got %v want %v", f, got, want)
					return
				}
				if got, want := wlMatchUntil(t, own, f), wlWant(ownSet, f); !slices.Equal(got, want) {
					t.Errorf("concurrent MatchUntil(%q) on own tree: got %v want %v", f, got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Concurrent readers over shapes that reach the package's fixed-size buffers and wide nodes:
// subjects past 256 bytes, filters past 32 tokens, and a terminal '*' at a node with more
// than 16 children. Readers are forced to overlap whatever GOMAXPROCS the run uses.
func TestWildcardCharInsideTokenConcurrentStructures(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(4))
	long := strings.Repeat("abcdefghij.", 30)
	deep := strings.Repeat("d.", 40)
	var subjects []string
	for _, sfx := range []string{"xapple", "xavocado", "x*1", "x*2", "y>z", "y>"} {
		subjects = append(subjects, long+sfx, deep+sfx)
	}
	for c := byte('A'); c <= 'Z'; c++ {
		subjects = append(subjects, "V.q"+string(c), "V.q"+string(c)+"*")
	}
	st, set := wlBuild(subjects...)
	filters := []string{long + "*", long + ">", long + "x*1", long + "y>", deep + "*", deep + ">", deep + "x*2",
		strings.Repeat("*.", 40) + "*", strings.Repeat("d.*.", 20) + "*", "V.*", "V.>", "V.q*", "*.qA*"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 4 * len(filters) {
				f := filters[(i+g)%len(filters)]
				if got, want := wlMatch(t, st, f), wlWant(set, f); !slices.Equal(got, want) {
					t.Errorf("concurrent Match(%q): got %v want %v", f, got, want)
					return
				}
				if got, want := wlMatchUntil(t, st, f), wlWant(set, f); !slices.Equal(got, want) {
					t.Errorf("concurrent MatchUntil(%q): got %v want %v", f, got, want)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	// The subject handed to a callback stays intact while a nested call runs.
	for oi, outer := range filters {
		got := []wlHit{}
		st.Match([]byte(outer), func(subj []byte, v *int) {
			before := string(subj)
			inner := filters[(oi+1+len(got))%len(filters)]
			if in, want := wlMatch(t, st, inner), wlWant(set, inner); !slices.Equal(in, want) {
				t.Fatalf("nested Match(%q) inside Match(%q): got %v want %v", inner, outer, in, want)
			}
			if string(subj) != before {
				t.Fatalf("Match(%q): subject changed during the callback from %q to %q", outer, before, subj)
			}
			got = append(got, wlHit{before, *v})
		})
		if want := wlWant(set, outer); !slices.Equal(wlSort(got), want) {
			t.Fatalf("Match(%q) with nested calls: got %v want %v", outer, got, want)
		}
	}
}

// A callback may run another match on the same tree; neither match may disturb the other.
func TestWildcardCharInsideTokenNestedMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 8765))
	for range 200 {
		subjects := make([]string, 1+r.IntN(12))
		for i := range subjects {
			subjects[i] = wlSubject(r, wlAlphaNarrow)
		}
		st, set := wlBuild(subjects...)
		outer, inner := wlFilter(r, subjects, wlAlphaNarrow), wlFilter(r, subjects, wlAlphaNarrow)
		wantInner := wlWant(set, inner)
		got := []wlHit{}
		st.Match([]byte(outer), func(subj []byte, v *int) {
			got = append(got, wlHit{string(subj), *v})
			if in := wlMatch(t, st, inner); !slices.Equal(in, wantInner) {
				t.Fatalf("nested Match(%q) inside Match(%q): got %v want %v", inner, outer, in, wantInner)
			}
		})
		if want := wlWant(set, outer); !slices.Equal(wlSort(got), want) {
			t.Fatalf("Match(%q) with nested Match(%q): got %v want %v", outer, inner, got, want)
		}
		got = got[:0]
		done := st.MatchUntil([]byte(outer), func(subj []byte, v *int) bool {
			got = append(got, wlHit{string(subj), *v})
			if in := wlMatchUntil(t, st, inner); !slices.Equal(in, wantInner) {
				t.Fatalf("nested MatchUntil(%q) inside MatchUntil(%q): got %v want %v", inner, outer, in, wantInner)
			}
			return true
		})
		if want := wlWant(set, outer); !done || !slices.Equal(wlSort(got), want) {
			t.Fatalf("MatchUntil(%q) with nested MatchUntil(%q): done=%v got %v want %v", outer, inner, done, got, want)
		}
		if len(wantInner) == 0 {
			continue
		}
		got = got[:0]
		st.Match([]byte(outer), func(subj []byte, v *int) {
			got = append(got, wlHit{string(subj), *v})
			if st.MatchUntil([]byte(inner), func([]byte, *int) bool { return false }) {
				t.Fatalf("nested MatchUntil(%q) stopped by its callback returned true", inner)
			}
		})
		if want := wlWant(set, outer); !slices.Equal(wlSort(got), want) {
			t.Fatalf("Match(%q) with a nested MatchUntil(%q) that stops early: got %v want %v", outer, inner, got, want)
		}
	}
}

// Literal filters must forget deleted subjects, and a match may be re-run after any update.
func TestWildcardCharInsideTokenDeleteAndEmpty(t *testing.T) {
	subjects := []string{"KV.a", "KV.a*", "KV.a*x", "KV.a>", "KV.apple", "x.>b", "a.**", "a.*b"}
	st, set := wlBuild(subjects...)
	for i, s := range subjects {
		wlCheck(t, st, set, s)
		wlCheck(t, st, set, "KV.*")
		st.Delete([]byte(s))
		delete(set, s)
		wlCheck(t, st, set, s)
		wlCheck(t, st, set, "KV.*")
		wlCheck(t, st, set, ">")
		if i%2 == 0 {
			st.Insert([]byte(s), 1000+i)
			set[s] = 1000 + i
			wlCheck(t, st, set, s)
		}
	}
	for _, s := range []string{"KV.z*", "KV.a>z", "q.**"} {
		wlCheck(t, st, set, s)
		st.Insert([]byte(s), 500)
		set[s] = 500
		wlCheck(t, st, set, s)
		wlCheck(t, st, set, "*.*")
	}
	for _, f := range []string{"KV.a*", "KV.z*", "*.a*", "KV.*"} {
		wlCheck(t, st, set, f)
	}
	st.Empty()
	set = map[string]int{}
	for _, s := range append(subjects, "KV.z*", "KV.a>z", "q.**", "*.a*", "KV.*") {
		wlCheck(t, st, set, s)
	}
	st.Insert([]byte("KV.a*"), 7)
	wlCheck(t, st, map[string]int{"KV.a*": 7}, "KV.a*")
}

// Subjects longer than 256 bytes and filters with more than 32 tokens.
func TestWildcardCharInsideTokenLongInputs(t *testing.T) {
	long := strings.Repeat("abcdefghij.", 30)
	st, set := wlBuild(long+"xapple", long+"xavocado", long+"x*1", long+"x*2", long+"y>z", long+"y>", "short.x")
	for _, f := range []string{
		long + "*", long + "xapple", long + "x*1", long + "x*", long + "x>", long + ">", long + "y>z",
		long + "y>", "*." + long[11:] + "*", "*." + long[11:] + "x*2", ">", long + "x*.>",
	} {
		wlCheck(t, st, set, f)
	}

	toks := make([]string, 40)
	for i := range toks {
		toks[i] = "t" + strings.Repeat("k", i%3)
	}
	mk := func(edit func([]string)) string {
		c := slices.Clone(toks)
		edit(c)
		return strings.Join(c, ".")
	}
	st, set = wlBuild(
		mk(func(c []string) {}),
		mk(func(c []string) { c[35] = "q" }),
		mk(func(c []string) { c[35] = "q*" }),
		mk(func(c []string) { c[39] = "end>" }),
		mk(func(c []string) { c[35], c[39] = "q*", "z" }),
	)
	for _, f := range []string{
		mk(func(c []string) { c[35] = "*" }),
		mk(func(c []string) { c[35] = "q*" }),
		mk(func(c []string) { c[35] = "*"; c[39] = "end>" }),
		mk(func(c []string) { c[39] = "*" }),
		strings.Join(toks[:34], ".") + ".>",
		mk(func(c []string) { c[33] = "*"; c[35] = "q*" }),
		mk(func(c []string) {}),
	} {
		wlCheck(t, st, set, f)
	}

	// Every other token a wildcard: well over 32 parts, and only the last token decides.
	many := mk(func(c []string) {
		for i := 0; i < len(c)-1; i += 2 {
			c[i] = "*"
		}
		c[len(c)-1] = "end>"
	})
	wlCheck(t, st, set, many)
	wlCheck(t, st, set, strings.TrimSuffix(many, "end>")+"*")
	wlCheck(t, st, set, strings.TrimSuffix(many, "end>")+"nope")
}

// The callback's pointer is the stored value: writes through it are visible to Find.
func TestWildcardCharInsideTokenValuePointer(t *testing.T) {
	st, set := wlBuild("KV.a*", "KV.a>x", "KV.apple", "KV.avocado", "x.**", "x.*>")
	for _, f := range []string{"KV.a*", "KV.a>x", "x.**", "KV.*", "x.*", ">", "KV.a*y"} {
		st.Match([]byte(f), func(_ []byte, v *int) { *v += 100 })
		ok := st.MatchUntil([]byte(f), func(_ []byte, v *int) bool { *v += 10; return true })
		if !ok {
			t.Fatalf("MatchUntil(%q) returned false", f)
		}
		for s := range set {
			if wlRefMatch(f, s) {
				set[s] += 110
			}
		}
		for s, want := range set {
			if p, found := st.Find([]byte(s)); !found || *p != want {
				t.Fatalf("after writes through Match/MatchUntil(%q): %q holds %v, want %d", f, s, p, want)
			}
		}
	}
}

// Results must follow inserts and deletes.
func TestWildcardCharInsideTokenAfterUpdates(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 8765))
	st, set := NewSubjectTree[int](), map[string]int{}
	var subjects []string
	for step := range 3000 {
		s := wlSubject(r, wlAlphaNarrow)
		if len(subjects) > 0 && r.IntN(3) == 0 {
			s = subjects[r.IntN(len(subjects))]
			st.Delete([]byte(s))
			delete(set, s)
		} else {
			st.Insert([]byte(s), step)
			set[s] = step
			subjects = append(subjects, s)
		}
		wlCheck(t, st, set, wlFilter(r, subjects, wlAlphaNarrow))
	}
}

// Shapes that only wider and longer trees reach. Node kinds grow 4 -> 10 -> 16 -> 48 -> 256
// children (the existing stree tests name each step), and a node can sit inside a token, where
// a terminal '*' or '>' has already consumed part of it. Filters with more than 32 tokens
// outgrow Match's 32-part array, subjects longer than 256 bytes outgrow its prefix buffer, and
// calls can run on more than one tree.

const wlWideAlpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz23456789-_+=~!@#$%^&()[]{}|:;,<?"

// wlInToken returns subjects that make pre (a partial token such as "N48.q") a node with
// about n children: one-token endings, the subject that ends at the node, endings with more
// tokens (one of them right after the node), a child node, literal '*' and '>' endings and a
// non-ASCII ending.
func wlInToken(pre string, n int) []string {
	out := []string{pre, pre + "*", pre + ">", pre + "\xc3\xa9", pre + "\xc3\xa8",
		pre + "0.x", pre + ".x", pre + "1a", pre + "1b", pre + "1*"}
	for i := 0; len(out) < n+3 && i < len(wlWideAlpha); i++ {
		out = append(out, pre+wlWideAlpha[i:i+1])
	}
	return out
}

// wlStops checks that MatchUntil makes exactly k calls and returns false when the k-th call
// asks it to stop, for every k up to the number of matches.
func wlStops(t testing.TB, st *SubjectTree[int], filter string, n int) {
	t.Helper()
	for k := 1; k <= n; k++ {
		calls := 0
		done := st.MatchUntil([]byte(filter), func([]byte, *int) bool { calls++; return calls < k })
		if calls != k || done {
			t.Fatalf("MatchUntil(%q) stopped at call %d: made %d calls, returned %v", filter, k, calls, done)
		}
	}
}

func wlWideTree() []string {
	var subjects []string
	for _, f := range [][2]any{{"N48.q", 30}, {"N48.qZ", 20}, {"N256.q", 70}, {"N256.qZ", 20}} {
		subjects = append(subjects, wlInToken(f[0].(string), f[1].(int))...)
	}
	return subjects
}

var wlWideFilters = []string{"N48.*", "N48.>", "N256.*", "N256.>", "N48.q*", "N256.q>", "*.*", ">",
	"N256.*.x", "N48.*.x", "*.q*", "N48.qZ*", "N256.qZ>", "*.qZ", "N256.q1*"}

func TestWildcardCharInsideTokenNodeInsideToken(t *testing.T) {
	st, set := wlBuild(wlWideTree()...)
	for _, f := range wlWideFilters {
		wlCheck(t, st, set, f)
		wlStops(t, st, f, len(wlWant(set, f)))
	}
	// Results follow inserts and deletes under a wide node.
	for i, s := range []string{"N256.q`", "N256.q\x7e\x7e", "N48.q`"} {
		st.Insert([]byte(s), 1000+i)
		set[s] = 1000 + i
		for _, f := range []string{"N256.*", "N48.*", "N256.>"} {
			wlCheck(t, st, set, f)
		}
	}
	for _, s := range []string{"N256.qA", "N256.q", "N48.qB", "N48.q"} {
		st.Delete([]byte(s))
		delete(set, s)
		for _, f := range []string{"N256.*", "N48.*", "N256.>", "N48.>"} {
			wlCheck(t, st, set, f)
		}
	}
}

// Calls nested in a callback, on the same tree or another one, and concurrent calls on one
// tree or on several, all over nodes inside tokens and long subjects.
func TestWildcardCharInsideTokenCallsOnTrees(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(4))
	long := func(c string) string { return "S." + c + strings.Repeat(c, 300) }
	subjects := append(wlWideTree(), long("a"), long("b"), long("c"))
	trees := make([]*SubjectTree[int], 4)
	var set map[string]int
	for i := range trees {
		trees[i], set = wlBuild(subjects...)
	}
	filters := append(slices.Clone(wlWideFilters), "S.*", "S.>", long("a"))

	// Nested calls: the outer subject stays intact and both calls see the right results.
	for _, inner := range []*SubjectTree[int]{trees[0], trees[1]} {
		for oi, outer := range filters {
			got := []wlHit{}
			trees[0].Match([]byte(outer), func(subj []byte, v *int) {
				before := string(subj)
				in := filters[(oi+1+len(got))%len(filters)]
				if g, w := wlMatch(t, inner, in), wlWant(set, in); !slices.Equal(g, w) {
					t.Fatalf("nested Match(%q) inside Match(%q): got %v want %v", in, outer, g, w)
				}
				if string(subj) != before {
					t.Fatalf("Match(%q): subject changed during a nested call from %q to %q", outer, before, subj)
				}
				got = append(got, wlHit{before, *v})
			})
			if want := wlWant(set, outer); !slices.Equal(wlSort(got), want) {
				t.Fatalf("Match(%q) with nested calls: got %v want %v", outer, got, want)
			}
		}
	}

	// Concurrent calls on one tree and on different trees; some filters are new to every call.
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := trees[g%len(trees)]
			<-start
			for i := range 3 * len(filters) {
				f := filters[(i+g)%len(filters)]
				if i%3 == 0 {
					f = "N48.q" + strconv.Itoa(g) + "." + strconv.Itoa(i) + ".*"
				}
				if got, want := wlMatch(t, st, f), wlWant(set, f); !slices.Equal(got, want) {
					t.Errorf("concurrent Match(%q): got %v want %v", f, got, want)
					return
				}
				if got, want := wlMatchUntil(t, st, f), wlWant(set, f); !slices.Equal(got, want) {
					t.Errorf("concurrent MatchUntil(%q): got %v want %v", f, got, want)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}

// Filters with more than 32 tokens that hold literal '*' and '>' inside tokens.
func TestWildcardCharInsideTokenManyTokens(t *testing.T) {
	toks := make([]string, 39)
	for i := range toks {
		toks[i] = "t" + strconv.Itoa(i)
	}
	base := strings.Join(toks, ".") + "."
	var subjects []string
	for _, end := range []string{"*a", "*ab", "b", ">b", ">bc", "c", "**", "**.z", "q.**", "q.z"} {
		subjects = append(subjects, base+end)
	}
	st, set := wlBuild(subjects...)
	for _, f := range []string{base + "*a", base + ">b", base + "**", base + "*", base + ">", base + "*.z",
		base + "q.*", strings.Repeat("*.", 39) + "*a", strings.Repeat("*.", 39) + ">b", base + "**.*", base + "*b"} {
		wlCheck(t, st, set, f)
		wlStops(t, st, f, len(wlWant(set, f)))
	}
}

// A caller may reuse its filter buffer once a call has returned.
func TestWildcardCharInsideTokenFilterBufferReuse(t *testing.T) {
	st, set := wlBuild("R89.a", "R89.b*", "X89.c")
	buf := []byte("R89.*")
	st.Match(buf, func([]byte, *int) {})
	st.MatchUntil(buf, func([]byte, *int) bool { return true })
	copy(buf, "X89.*")
	wlCheck(t, st, set, "R89.*")
	wlCheck(t, st, set, "X89.*")
}
