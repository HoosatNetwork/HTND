package profiling

import "testing"

func TestListenAddress(t *testing.T) {
	tests := []struct {
		profile string
		want    string
		wantErr bool
	}{
		// A bare port keeps --profile's original meaning: every interface.
		{profile: "6061", want: ":6061"},
		{profile: "127.0.0.1:6061", want: "127.0.0.1:6061"},
		{profile: "localhost:6062", want: "localhost:6062"},
		{profile: "[::1]:6061", want: "[::1]:6061"},
		{profile: ":6061", want: ":6061"},
		{profile: "1023", wantErr: true},
		{profile: "65536", wantErr: true},
		{profile: "127.0.0.1:80", wantErr: true},
		{profile: "127.0.0.1", wantErr: true},
		{profile: "127.0.0.1:abc", wantErr: true},
		{profile: "::1:6061", wantErr: true},
		{profile: "abc", wantErr: true},
	}
	for _, test := range tests {
		got, err := ListenAddress(test.profile)
		if test.wantErr {
			if err == nil {
				t.Errorf("ListenAddress(%q) = %q, want an error", test.profile, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("ListenAddress(%q) = %q, %v, want %q", test.profile, got, err, test.want)
		}
	}
}
