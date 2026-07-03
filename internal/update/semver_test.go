package update

import "testing"

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in                   string
		wantMaj, wantMin, wp int
		wantPre              string
		wantErr              bool
	}{
		{"v0.4.1", 0, 4, 1, "", false},
		{"1.2.3", 1, 2, 3, "", false},
		{"  v2.0.0  ", 2, 0, 0, "", false},
		{"0.4.0-rc1", 0, 4, 0, "rc1", false},
		{"1.2.3+build.5", 1, 2, 3, "", false}, // build metadata dropped
		{"1.2", 0, 0, 0, "", true},
		{"1.2.x", 0, 0, 0, "", true},
		{"", 0, 0, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseSemver(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error for %q", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSemver(%q): %v", tt.in, err)
			}
			if got.major != tt.wantMaj || got.minor != tt.wantMin || got.patch != tt.wp || got.pre != tt.wantPre {
				t.Errorf("parseSemver(%q) = %+v, want %d.%d.%d-%q", tt.in, got, tt.wantMaj, tt.wantMin, tt.wp, tt.wantPre)
			}
		})
	}
}

func TestCompareAndBump(t *testing.T) {
	tests := []struct {
		from, to string
		wantBump Bump
	}{
		{"0.3.0", "0.3.1", BumpPatch},
		{"0.3.0", "0.4.0", BumpMinor},
		{"0.3.0", "1.0.0", BumpMajor},
		{"0.3.1", "0.3.1", BumpNone},
		{"0.4.0", "0.3.9", BumpNone},
		{"0.4.0-rc1", "0.4.0", BumpPatch}, // final > its prerelease
		{"0.4.0", "0.4.0-rc1", BumpNone},  // prerelease < final
	}
	for _, tt := range tests {
		t.Run(tt.from+"->"+tt.to, func(t *testing.T) {
			from, err := parseSemver(tt.from)
			if err != nil {
				t.Fatalf("from: %v", err)
			}
			to, err := parseSemver(tt.to)
			if err != nil {
				t.Fatalf("to: %v", err)
			}
			if got := bumpBetween(from, to); got != tt.wantBump {
				t.Errorf("bumpBetween(%q,%q) = %q, want %q", tt.from, tt.to, got, tt.wantBump)
			}
		})
	}
}
