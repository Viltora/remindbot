package telegram

import (
	"testing"

	"remindbot/internal/storage"
)

func TestWhoTagged(t *testing.T) {
	names := map[string]string{"ks": "Катя", "am": "Лена", "mail": "someone@example.com", "blank": ""}
	users := map[string]storage.User{"ks": {TgID: 1, Username: "kseniya"}, "an": {TgID: 2, FirstName: "Аня"}}
	for _, tc := range []struct {
		ids  []string
		want string
	}{
		{[]string{"ks", "am"}, "@kseniya, Лена"},
		{[]string{"an"}, `<a href="tg://user?id=2">Аня</a>`},
		{[]string{"mail"}, "исполнитель"},
		{[]string{"mail", "am"}, "Лена"},
		{[]string{"blank", "unknown"}, "исполнитель"},
	} {
		if got := whoTagged(tc.ids, names, users); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.ids, got, tc.want)
		}
	}
}

func TestJoinNamesHidesEmails(t *testing.T) {
	names := map[string]string{"mail": "someone@example.com", "am": "Лена <3"}
	if got := joinNames([]string{"mail"}, names); got != "исполнитель" {
		t.Errorf("e-mail only: %q", got)
	}
	if got := joinNames([]string{"mail", "am"}, names); got != "Лена &lt;3" {
		t.Errorf("escaped name: %q", got)
	}
	if got := joinNames(nil, names); got != "" {
		t.Errorf("no assignees: %q", got)
	}
}

func TestPeopleDataFitsTelegram(t *testing.T) {
	d := peopleData(cbPeopleLink, 9_999_999_999_999, "c8e6813f-5519-4bc4-8080-82388a0c4f20")
	if len(d) > 64 {
		t.Errorf("callback data %q is %d bytes, Telegram allows 64", d, len(d))
	}
}

func TestGreetingForEveryPurpose(t *testing.T) {
	for p := range purposeLabels {
		if _, ok := greetAbout[p]; !ok && p != storage.PurposeNone {
			t.Errorf("no greeting for chat purpose %q", p)
		}
	}
}
