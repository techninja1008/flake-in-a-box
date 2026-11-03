package storepath

import "testing"

func TestMakeSourcePath(t *testing.T) {
	h, err := ParseSHA256("sha256:1rlklj7jgxf96zmz5z0ha9qw1mdal3vhwsdd40pmv0b75xgc7lz1")
	if err != nil {
		t.Fatal(err)
	}
	got := MakeSourcePath("libunistring-1.4.2", h, nil, true)
	if want := "/nix/store/i19krds6qaq5cmz75mbcmjilnyyxg5ms-libunistring-1.4.2"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if FormatNix32(h) != "sha256:1rlklj7jgxf96zmz5z0ha9qw1mdal3vhwsdd40pmv0b75xgc7lz1" {
		t.Fatal("nix32 roundtrip")
	}
}
