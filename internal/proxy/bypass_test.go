package proxy

import "testing"

func TestShouldBypassMITM(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{host: "pypi.org", want: true},
		{host: "files.pythonhosted.org", want: true},
		{host: "foo.pythonhosted.org", want: true},
		{host: "PyPI.ORG", want: true},
		{host: "api.anthropic.com", want: false},
		{host: "pythonhosted.org.evil.com", want: false},
	}

	for _, tt := range tests {
		if got := shouldBypassMITM(tt.host); got != tt.want {
			t.Errorf("shouldBypassMITM(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}
