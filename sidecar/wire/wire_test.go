package wire

import "testing"

func TestParseVoteOption(t *testing.T) {
	cases := []struct {
		in   string
		want VoteOption
		err  bool
	}{
		{"yes", OptionYes, false},
		{"YES", OptionYes, false},
		{"no", OptionNo, false},
		{"abstain", OptionAbstain, false},
		{"no_with_veto", OptionNoWithVeto, false},
		{"no-with-veto", OptionNoWithVeto, false},
		{"NO_WITH_VETO", OptionNoWithVeto, false},
		{"", 0, true},
		{"maybe", 0, true},
	}
	for _, c := range cases {
		got, err := ParseVoteOption(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseVoteOption(%q): want err, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVoteOption(%q): unexpected err: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseVoteOption(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
