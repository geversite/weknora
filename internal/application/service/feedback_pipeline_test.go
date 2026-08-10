package service

import "testing"

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare json", `{"a":1}`, `{"a":1}`},
		{"json in fences", "```json\n{\"ok\":true}\n```", `{"ok":true}`},
		{"json after prose", `Sure. {"provides_new_info": true, "reason": "it is a fact"}`, `{"provides_new_info": true, "reason": "it is a fact"}`},
		{"nested braces", `{"a":{"b":1}}`, `{"a":{"b":1}}`},
		{"string with brace", `{"a":"{ not json }"}`, `{"a":"{ not json }"}`},
		{"escaped quote", `{"a":"say \"hi\""}`, `{"a":"say \"hi\""}`},
		{"no braces", `nothing here`, `{}`},
		{"leading text then empty object", `x { } y`, `{ }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSON(tc.in)
			if got != tc.want {
				t.Fatalf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
