package telegram

import (
	"strings"
	"testing"

	"remindbot/internal/config"
)

func TestHelpText(t *testing.T) {
	off := helpText(config.Features{})
	for _, unwanted := range []string{"18:20, 18:40", "утверди", "Выложено?» [✅ Да]", "{"} {
		if strings.Contains(off, unwanted) {
			t.Errorf("all off: unexpected %q", unwanted)
		}
	}
	for _, want := range []string{"🔔 за 60 и 30 мин.", "<b>Выключено</b>", "пришли материал", "пн, ср, пт 16:00", "пн 13:00", "Через час одно напоминание"} {
		if !strings.Contains(off, want) {
			t.Errorf("all off: no %q", want)
		}
	}

	on := helpText(config.Features{SalesPoll: true, PublishQuestions: true})
	for _, want := range []string{"18:20, 18:40", "«Выложено?» [✅ Да] [❌ Нет], до 4 раз."} {
		if !strings.Contains(on, want) {
			t.Errorf("all on: no %q", want)
		}
	}
	if strings.Contains(on, "<b>Выключено</b>") {
		t.Error("all on: unexpected «Выключено»")
	}
}
