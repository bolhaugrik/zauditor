package core

import (
	"reflect"
	"testing"
)

type fake struct {
	id string
	w  float64
}

func (f *fake) ID() string      { return f.id }
func (f *fake) Name() string    { return "fake " + f.id }
func (f *fake) Weight() float64 { return f.w }
func (f *fake) Analyze(*RepoContext) DimensionResult {
	return DimensionResult{Score: 1}
}

func TestRegisterAndAllIsSortedByID(t *testing.T) {
	resetRegistryForTest()
	Register(&fake{id: "zeta"})
	Register(&fake{id: "alpha"})
	Register(&fake{id: "mid"})

	var got []string
	for _, a := range All() {
		got = append(got, a.ID())
	}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("All() = %v, want %v (registration order must not leak into output)", got, want)
	}
}

func TestRegisterDuplicateIDPanics(t *testing.T) {
	resetRegistryForTest()
	Register(&fake{id: "dup"})
	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate ID must panic")
		}
	}()
	Register(&fake{id: "dup"})
}

func TestGet(t *testing.T) {
	resetRegistryForTest()
	Register(&fake{id: "size"})
	if a, ok := Get("size"); !ok || a.ID() != "size" {
		t.Fatalf("Get(size) = %v, %v", a, ok)
	}
	if _, ok := Get("nope"); ok {
		t.Fatal("Get returned an analyzer for an unknown ID")
	}
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name string
		sel  Selection
		want []string
	}{
		{"default is everything", Selection{}, []string{"a", "b", "c"}},
		{"only", Selection{Only: []string{"b"}}, []string{"b"}},
		{"skip", Selection{Skip: []string{"a", "c"}}, []string{"b"}},
		{"only wins, skip subtracts", Selection{Only: []string{"a", "b"}, Skip: []string{"a"}}, []string{"b"}},
		{"config disables", Selection{Disabled: map[string]bool{"b": true}}, []string{"a", "c"}},
		{"disabled beats only", Selection{Only: []string{"b"}, Disabled: map[string]bool{"b": true}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetRegistryForTest()
			Register(&fake{id: "a"})
			Register(&fake{id: "b"})
			Register(&fake{id: "c"})

			chosen, unknown := Select(tt.sel)
			if len(unknown) > 0 {
				t.Fatalf("unexpected unknown IDs: %v", unknown)
			}
			var got []string
			for _, a := range chosen {
				got = append(got, a.ID())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Select() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectReportsUnknownIDs(t *testing.T) {
	resetRegistryForTest()
	Register(&fake{id: "size"})

	chosen, unknown := Select(Selection{Only: []string{"siez"}})
	if chosen != nil {
		t.Fatal("a typo must not silently run a subset of analyzers")
	}
	if !reflect.DeepEqual(unknown, []string{"siez"}) {
		t.Fatalf("unknown = %v, want [siez]", unknown)
	}
}

func TestClamp01(t *testing.T) {
	for _, tt := range []struct{ in, want float64 }{
		{-1, 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {2, 1},
	} {
		if got := Clamp01(tt.in); got != tt.want {
			t.Errorf("Clamp01(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
