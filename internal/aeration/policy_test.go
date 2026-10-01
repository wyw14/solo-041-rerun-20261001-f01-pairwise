package aeration

import (
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

func TestPolicyBookRequiresLines(t *testing.T) {
	if _, err := NewPolicyBook(nil); err == nil {
		t.Fatal("expected an error for an empty line set")
	}
}

func TestPolicyBookDefaults(t *testing.T) {
	book, err := NewPolicyBook([]model.LineID{"line-1", "line-2"})
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := book.Get("line-1")
	if !ok {
		t.Fatal("expected a default policy for line-1")
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy must be valid: %v", err)
	}
	if _, ok := book.Get("line-9"); ok {
		t.Fatal("expected no policy for an unknown line")
	}
}

func TestPolicyBookConfigure(t *testing.T) {
	book, err := NewPolicyBook([]model.LineID{"line-1"})
	if err != nil {
		t.Fatal(err)
	}
	custom := LinePolicy{
		LineID: "line-1", TargetMin: 1.5, TargetMax: 3.0, MaxStep: 2.5,
		SafeFrequency: 30, IdleFrequency: 12, SampleMaxAge: 90 * time.Second,
	}
	if err := book.Configure(custom); err != nil {
		t.Fatal(err)
	}
	stored, _ := book.Get("line-1")
	if stored != custom {
		t.Fatalf("expected stored policy %+v, got %+v", custom, stored)
	}
}

func TestPolicyBookRejectsInvalidPolicies(t *testing.T) {
	book, err := NewPolicyBook([]model.LineID{"line-1"})
	if err != nil {
		t.Fatal(err)
	}
	base := DefaultPolicy("line-1")
	cases := map[string]LinePolicy{
		"unknown line":   {LineID: "line-9", TargetMin: 2, TargetMax: 3.5, MaxStep: 4, SafeFrequency: 36, SampleMaxAge: time.Minute},
		"inverted range": {LineID: "line-1", TargetMin: 3.5, TargetMax: 2, MaxStep: 4, SafeFrequency: 36, SampleMaxAge: time.Minute},
		"zero step":      {LineID: "line-1", TargetMin: 2, TargetMax: 3.5, MaxStep: 0, SafeFrequency: 36, SampleMaxAge: time.Minute},
		"safe too high":  {LineID: "line-1", TargetMin: 2, TargetMax: 3.5, MaxStep: 4, SafeFrequency: 61, SampleMaxAge: time.Minute},
		"age too small":  {LineID: "line-1", TargetMin: 2, TargetMax: 3.5, MaxStep: 4, SafeFrequency: 36, SampleMaxAge: 0},
	}
	for name, policy := range cases {
		if err := book.Configure(policy); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
	if err := book.Configure(base); err != nil {
		t.Fatalf("baseline policy must configure cleanly: %v", err)
	}
}
