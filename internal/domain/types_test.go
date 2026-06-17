package domain

import "testing"

func TestIsOnboardingCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "plain", body: "/onboard", want: true},
		{name: "with arguments", body: "/onboard update my profile", want: true},
		{name: "telegram bot mention", body: "/onboard@OpenCTOBot", want: true},
		{name: "telegram bot mention with arguments", body: "/onboard@OpenCTOBot update my profile", want: true},
		{name: "empty bot mention", body: "/onboard@", want: false},
		{name: "different command", body: "/onboarding", want: false},
		{name: "empty", body: "   ", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsOnboardingCommand(tt.body); got != tt.want {
				t.Fatalf("IsOnboardingCommand(%q) = %t, want %t", tt.body, got, tt.want)
			}
		})
	}
}
