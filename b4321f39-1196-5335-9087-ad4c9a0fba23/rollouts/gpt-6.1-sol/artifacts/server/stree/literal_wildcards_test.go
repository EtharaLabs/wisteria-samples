package stree

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Model whole tokens, independently of the tree's compressed paths and matcher.
func literalFilterMatches(filter, subject string) bool {
	for {
		token, rest, more := strings.Cut(filter, ".")
		if token == ">" {
			return subject != ""
		}
		value, tail, next := strings.Cut(subject, ".")
		if token != "*" && token != value {
			return false
		}
		if !more || !next {
			return more == next
		}
		filter, subject = rest, tail
	}

}

// No callback order is assumed, including between Match and MatchUntil.
func checkLiteralFilter(st *SubjectTree[int], contents map[string]int, filter string, stop int) error {
	want := 0
	for s := range contents {
		if literalFilterMatches(filter, s) {
			want++
		}
	}
	seen := make(map[string]bool)
	var problem error
	cb := func(subject []byte, value *int) bool {
		s := string(subject) // callback memory need not survive the call
		expected, exists := contents[s]
		p, found := st.Find([]byte(s))
		if !exists || !literalFilterMatches(filter, s) || seen[s] || !found || p != value || value == nil {
			problem = fmt.Errorf("filter %q: unexpected/duplicate subject or value pointer for %q", filter, s)
		} else if *value != expected {
			problem = fmt.Errorf("filter %q: %q value = %d, want %d", filter, s, *value, expected)
		}
		seen[s] = true
		return stop <= 0 || len(seen) < stop
	}
	completed := true
	if stop < 0 {
		st.Match([]byte(filter), func(s []byte, v *int) { cb(s, v) })
	} else {
		completed = st.MatchUntil([]byte(filter), cb)
	}
	if problem != nil {
		return problem
	}
	n := want
	if stop > 0 && n >= stop {
		n = stop
	}
	if len(seen) != n || completed != !(stop > 0 && want >= stop) {
		return fmt.Errorf("filter %q stop %d: got %d callbacks, completed %v; want %d, %v", filter, stop, len(seen), completed, n, !(stop > 0 && want >= stop))
	}
	return nil
}

func literalCorpus() ([]string, []string) {
	tokens := []string{"a", "b", "apple", "a*", "*a", "**", "a>", ">b", ">>", "a*b", "a>b", "*>"}
	subjects := []string{"KV.a", "KV.apple", "KV.avocado", "KV.banana"}
	filters := []string{">", "*", "KV.a*", "KV.*", "KV.>", "absent.>"}
	for _, a := range tokens {
		subjects = append(subjects, a, "KV."+a, a+".tail", "root."+a+".tail")
		filters = append(filters, a, "KV."+a, a+".*", a+".>", "*."+a, "root."+a+".*", "*."+a+".>")
		for _, b := range tokens {
			subjects = append(subjects, a+"."+b+".end", "root."+a+"."+b)
			filters = append(filters, a+"."+b+".*", "*."+a+"."+b, a+".*.end", a+"."+b+".>")
		}
	}
	// Exercise large fanouts without depending on any particular node layout.
	for c := 33; c <= 126; c++ {
		if c == '.' || c == '*' || c == '>' {
			continue
		}
		s := "fan." + string(rune(c))
		subjects = append(subjects, s+"a*", s+"apple", s+">b", s+".a*.end")
		filters = append(filters, s+"a*", s+">b", s+".*.>")
	}
	for _, n := range []int{31, 64, 257, 1024} {
		prefix := strings.Repeat("p", n)
		deep := strings.Repeat("x.", n)
		subjects = append(subjects, prefix+".a*.end", prefix+".apple.end", deep+"**.end", deep+">b.end")
		filters = append(filters, prefix+".a*.*", prefix+".*.>", deep+"**.*", deep+"*.end", deep+">b.>")
	}
	return subjects, filters
}

func TestLiteralWildcardTreeShapes(t *testing.T) {
	subjects, filters := literalCorpus()
	for _, reverse := range []bool{false, true} {
		st := NewSubjectTree[int]()
		contents := make(map[string]int)
		check := func(fs []string) {
			t.Helper()
			for _, f := range fs {
				for _, stop := range []int{-1, 0, 2} {
					if err := checkLiteralFilter(st, contents, f, stop); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		check(filters)
		// Check leaves and small trees as well as the fully populated tree.
		for i := range subjects {
			j := i
			if reverse {
				j = len(subjects) - 1 - i
			}
			s := subjects[j]
			st.Insert([]byte(s), j)
			contents[s] = j
			if i < 6 {
				check(filters[:6])
			}
		}
		check(append(filters, subjects...))
		for i, s := range subjects {
			if i%3 != 0 {
				st.Delete([]byte(s))
				delete(contents, s)
			} else {
				st.Insert([]byte(s), -i)
				contents[s] = -i
			}
		}
		check(filters)
		st.Empty()
		clear(contents)
		check(filters)
		for i, s := range subjects {
			st.Insert([]byte(s), i)
			contents[s] = i
		}
		check(filters)
	}
}

func TestLiteralWildcardCallbackWrites(t *testing.T) {
	st := NewSubjectTree[int]()
	contents := map[string]int{"a*.one": 1, "a*.two": 2, "apple.one": 3, "**.one": 4, ">b.two": 5}
	for s, v := range contents {
		st.Insert([]byte(s), v)
	}
	for _, until := range []bool{false, true} {
		cb := func(s []byte, v *int) bool {
			key := string(s)
			p, ok := st.Find(s)
			if !ok || p != v {
				t.Fatalf("callback pointer for %q differs from Find", key)
			}
			*v += 10
			contents[key] += 10
			return true
		}
		if until {
			st.MatchUntil([]byte("*.one"), cb)
		} else {
			st.Match([]byte("*.one"), func(s []byte, v *int) { cb(s, v) })
		}
		for _, f := range []string{">", "a*.>", "**.*", ">b.*"} {
			if err := checkLiteralFilter(st, contents, f, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLiteralWildcardReentrantAndConcurrent(t *testing.T) {
	subjects, filters := literalCorpus()
	trees := []*SubjectTree[int]{NewSubjectTree[int](), NewSubjectTree[int]()}
	models := []map[string]int{make(map[string]int), make(map[string]int)}
	for k, st := range trees {
		for i, s := range subjects {
			if k == 0 || i%2 == 0 {
				st.Insert([]byte(s), i)
				models[k][s] = i
			}
		}
	}
	// Nested calls must not overwrite outer traversal/filter state. Copy the
	// outer subject before nesting, since its slice is valid only in the callback.
	for _, outerUntil := range []bool{false, true} {
		seen := make(map[string]bool)
		cb := func(s []byte, v *int) bool {
			key := string(s)
			p, ok := trees[0].Find([]byte(key))
			if !ok || p != v || !literalFilterMatches("root.*.*", key) {
				t.Errorf("unexpected outer callback %q", key)
			}
			if seen[key] {
				t.Errorf("duplicate outer subject %q", key)
			}
			seen[key] = true
			for _, f := range []string{"a*.>", "*.**.*", ">b.*", ">"} {
				for _, stop := range []int{-1, 0, 1} {
					if err := checkLiteralFilter(trees[0], models[0], f, stop); err != nil {
						t.Error(err)
					}
				}
			}
			return true
		}
		if outerUntil {
			trees[0].MatchUntil([]byte("root.*.*"), cb)
		} else {
			trees[0].Match([]byte("root.*.*"), func(s []byte, v *int) { cb(s, v) })
		}
		want := 0
		for s := range models[0] {
			if literalFilterMatches("root.*.*", s) {
				want++
			}
		}
		if len(seen) != want {
			t.Fatalf("nested traversal visited %d, want %d", len(seen), want)
		}
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i, f := range filters {
				k := (i + w) % 2
				if err := checkLiteralFilter(trees[k], models[k], f, []int{-1, 0, 1, 2}[(i+w)%4]); err != nil {
					t.Error(err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestLiteralWildcardSmallTrees(t *testing.T) {
	for _, literal := range []string{"a*", "*a", "**", "a>", ">b", ">>", "a*b", "a>b", "*>"} {
		for _, prefix := range []string{"", "KV.", "root.branch."} {
			st := NewSubjectTree[int]()
			contents := make(map[string]int)
			subjects := []string{prefix + literal, prefix + "apple", prefix + "a", prefix + literal + ".child", prefix + literal + "suffix", prefix + "banana"}
			filters := []string{prefix + literal, prefix + literal + ".*", prefix + literal + ".>", prefix + "*", prefix + ">"}
			for i, s := range subjects {
				st.Insert([]byte(s), i)
				contents[s] = i
				for _, f := range filters {
					for _, stop := range []int{-1, 0, 1, 2} {
						if err := checkLiteralFilter(st, contents, f, stop); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
	}
}
