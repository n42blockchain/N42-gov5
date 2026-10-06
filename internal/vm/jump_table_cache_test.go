package vm

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

// g65CacheRules builds a minimal set of chain rules for exercising the
// jump table cache key derivation and construction paths.
func g65CacheRules(fork string) *params.Rules {
	r := &params.Rules{}
	switch fork {
	case "frontier":
	case "homestead":
		r.IsHomestead = true
	case "pectra":
		r.IsHomestead = true
		r.IsTangerineWhistle = true
		r.IsSpuriousDragon = true
		r.IsByzantium = true
		r.IsConstantinople = true
		r.IsPetersburg = true
		r.IsIstanbul = true
		r.IsBerlin = true
		r.IsLondon = true
		r.IsShanghai = true
		r.IsCancun = true
		r.IsPectra = true
	case "fusaka":
		r.IsHomestead = true
		r.IsTangerineWhistle = true
		r.IsSpuriousDragon = true
		r.IsByzantium = true
		r.IsConstantinople = true
		r.IsPetersburg = true
		r.IsIstanbul = true
		r.IsBerlin = true
		r.IsLondon = true
		r.IsShanghai = true
		r.IsCancun = true
		r.IsPectra = true
		r.IsOsaka = true
		r.IsFusaka = true
	}
	return r
}

func TestGetCachedJumpTableBuildsAndReusesTable(t *testing.T) {
	t.Parallel()

	rules := g65CacheRules("frontier")
	first := GetCachedJumpTable(1, rules)
	second := GetCachedJumpTable(1, rules)

	if first[STOP] != second[STOP] {
		t.Fatalf("expected the cached frontier table to be reused across calls")
	}

	homestead := GetCachedJumpTable(1, g65CacheRules("homestead"))
	if homestead[STOP] == nil {
		t.Fatalf("expected a populated table for the homestead fork")
	}
}

func TestGetCachedJumpTableCoversAllForkBranches(t *testing.T) {
	t.Parallel()

	for _, fork := range []string{"frontier", "homestead", "pectra", "fusaka"} {
		rules := g65CacheRules(fork)
		jt := GetCachedJumpTable(1, rules)
		if jt[STOP] == nil {
			t.Fatalf("fork %s: expected a populated jump table", fork)
		}
	}
}

func TestJumpTableCacheKeyIsStableAndDistinguishesForks(t *testing.T) {
	t.Parallel()

	if key := jumpTableCacheKey(g65CacheRules("frontier")); key != "frontier" {
		t.Fatalf("frontier key = %q, want %q", key, "frontier")
	}

	a := jumpTableCacheKey(g65CacheRules("pectra"))
	b := jumpTableCacheKey(g65CacheRules("fusaka"))
	if a == b {
		t.Fatalf("expected distinct keys for pectra (%q) and fusaka (%q)", a, b)
	}
	if jumpTableCacheKey(g65CacheRules("pectra")) != a {
		t.Fatalf("expected the same rules to always produce the same key")
	}
}

func TestNewJumpTableForRulesSelectsEachFork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		rules params.Rules
	}{
		{"frontier", params.Rules{}},
		{"homestead", params.Rules{IsHomestead: true}},
		{"tangerine", params.Rules{IsHomestead: true, IsTangerineWhistle: true}},
		{"spurious", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true}},
		{"byzantium", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true}},
		{"constantinople", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true}},
		{"istanbul", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true}},
		{"berlin", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}},
		{"london", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true}},
		{"shanghai", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true}},
		{"cancun", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true}},
		{"pectra", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true}},
		{"osaka", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true}},
		{"osaka-eof", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true, IsEOF: true}},
		{"fusaka", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true, IsFusaka: true}},
		{"fusaka-eof", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true, IsFusaka: true, IsEOF: true}},
		{"glamsterdam", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true, IsFusaka: true, IsGlamsterdam: true}},
		{"glamsterdam-eof", params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true, IsFusaka: true, IsGlamsterdam: true, IsEOF: true}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jt := newJumpTableForRules(&tc.rules)
			if jt[STOP] == nil {
				t.Fatalf("fork %s: STOP must be defined in every instruction set", tc.name)
			}
		})
	}
}

func TestPrewarmJumpTablesPopulatesCache(t *testing.T) {
	PrewarmJumpTables()

	rules := params.Rules{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true}
	key := jumpTableCacheKey(&rules)

	jumpTableCache.mu.RLock()
	_, ok := jumpTableCache.tables[key]
	jumpTableCache.mu.RUnlock()
	if !ok {
		t.Fatalf("expected PrewarmJumpTables to have cached the osaka fork table")
	}
}
