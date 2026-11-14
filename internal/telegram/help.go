package telegram

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/config"
)

// helpText describes everything the bot does, the switched-off scenarios
// left out. Keep it in sync with the scenarios (sales.go, plan.go,
// tasks_schedule.go, quiet.go) and docs/bot.md.
func helpText(f config.Features) string {
	sales, asks := helpSalesPoll, helpPublishAsk
	if !f.SalesPoll {
		sales = ""
	}
	if !f.PublishQuestions {
		asks = "."
	}
	off := helpOff(f)
	return strings.NewReplacer("{sales}", sales, "{asks}", asks, "{off}", off).Replace(helpTemplate)
}

// helpOff lists the switched-off scenarios.
func helpOff(f config.Features) string {
	var off []string
	if !f.SalesPoll {
		off = append(off, "вечерний опрос продажников «отправил?»")
	}
	if !f.PublishQuestions {
		off = append(off, "«Выложено?» после публикации")
	}
	if len(off) == 0 {
		return ""
	}
	return "<b>Выключено</b> (включается в .env)\n" + strings.Join(off, "; ") + "\n\n"
}

const helpSalesPoll = `<b>Опрос продажников</b> (пн–пт)
18:00 — «@продажник, пришли отчёт за сегодня руководителю в личку»
18:20, 18:40 — «Отправил?» [✅ Да] [❌ Нет] — нажать может только тот, кого тегнули
до 20:40 — ещё можно нажать «Да»; затем итог дня всем админам в личку

`

const helpPublishAsk = `; через 30 мин после публикации «Выложено?» [✅ Да] [❌ Нет], до 4 раз.`

const helpTemplate = `❓ <b>Что умеет бот</b>

<b>Команды</b>
/today — сегодня: публикации, материал, отчёты продажников
/week — неделя с понедельника: что выложено, что впереди, по дням, типам и людям
/month — месяц (пока не готов)
/admins — роли: админы, 📌 основной, sales
/chats — групповые чаты: 📝 контент и 💼 продажи
/status — версия, база, YouGile, пользователи, групповые чаты
/whoami — твой ID (в группе — ещё и ID чата)
/help — эта справка

<b>Роли</b>
• Новый человек пишет боту /start → тебе приходит уведомление с кнопками [⭐ Админ] [💼 Sales]. То же — в /admins.
• Роль можно дать только тому, кто сам написал боту.
• 📌 Основной админ — его одного бот тегает в чатах и называет продажникам. Первый админ становится основным сам.
• Посторонним бот не отвечает.

<b>Отчёт продажников</b> (в чат продаж с тегом; нет чата — в личку)
пн, ср, пт 16:00 — «пора заполнить отчёт», без кнопок и вопросов

{sales}<b>Контент-план</b> (в чат контента)
пн 13:00 — «заполните контент-план на неделю», админа не тегает

<b>Публикации из YouGile</b> (в чат контента; доска «ПУБЛИКАЦИИ ВСЕГО», сверка раз в 30 мин с 06:00 до 23:00)
• Пост, рилс: за сутки (с выходных — в пятницу) «пришли материал»; засчитывается файл или ссылка ответом на сообщение бота. Через час одно напоминание, админа не тегает.
• Все типы: 🔔 за 60 и 30 мин{asks}
• Исполнитель из YouGile — по имени, без тега (связки с Telegram пока нет). Нет исполнителя — основной админ.
• В YouGile бот ничего не меняет, только читает.

<b>Тихие часы</b>
22:00–07:00 — по расписанию ничего не отправляется, перенос на 07:00.

<b>Групповые чаты</b>
• Добавить бота в группу можешь ты или админ — тебе придёт вопрос: [📝 Контент] [💼 Продажи]. Поменять — в /chats.
• Если бота добавил кто-то другой — бот сам выйдет и предупредит тебя.
• Бот запоминает, кого заметил в чатах — см. /status.

{off}<b>Скоро</b>
Связка людей YouGile ↔ Telegram и теги; отчёт за неделю по воскресеньям в 21:00; Excel за месяц.

Подробно: docs/bot.md и блок-схемы docs/bot-flow.pdf.`

func (t *Bot) onHelp(ctx context.Context, _ *bot.Bot, update *models.Update) {
	t.reply(ctx, update.Message, helpText(t.features))
}
