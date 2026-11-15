package telegram

import (
	"context"
	"fmt"
	"html"
	"slices"
	"strings"
	"time"

	"remindbot/internal/storage"
)

// Reports for admins: /today and /week. The texts are built by pure functions
// from tasks already loaded, so they can be tested without Telegram. Times are
// taken in now's location, the bot's time zone.

// pubState is where a publication stands.
type pubState int

const (
	pubDone pubState = iota
	pubNotDone
	pubNoAnswer
	pubWaiting  // asked, or about to be asked
	pubAhead    // not published yet
	pubNotAsked // the time to ask passed without a question
	pubPassed   // the time passed; "published?" questions are off
	pubStates
)

type stateLabel struct{ icon, name string }

var pubLabels = [pubStates]stateLabel{
	pubDone:     {"✅", "выложено"},
	pubNotDone:  {"❌", "не выложено"},
	pubNoAnswer: {"⚠️", "нет ответа"},
	pubWaiting:  {"⏳", "ждём ответа"},
	pubAhead:    {"🕓", "впереди"},
	pubNotAsked: {"🔕", "не спрашивали"},
	pubPassed:   {"🕘", "время прошло"},
}

// pubStateOf says where the publication stands; asks tells whether the bot
// asks "published?" (FEATURE_PUBLISH_QUESTIONS). With the questions off a
// publication is only ahead or past: answers given while they were on are
// not shown, so the reports do not look as if the bot still asked.
func pubStateOf(task storage.Task, now time.Time, asks bool) pubState {
	if !asks {
		if now.Before(task.PublishAt) {
			return pubAhead
		}
		return pubPassed
	}
	switch task.Status {
	case storage.PublishDone:
		return pubDone
	case storage.PublishNotDone:
		return pubNotDone
	case storage.PublishNoAnswer:
		return pubNoAnswer
	}
	p := task.PublishAt.In(now.Location())
	switch {
	case now.Before(p):
		return pubAhead
	case !asks:
		return pubPassed
	case task.Attempts > 0:
		return pubWaiting
	case now.Sub(outsideQuietHours(p.Add(taskStep))) <= askCatchUp:
		return pubWaiting
	}
	return pubNotAsked
}

// matState is where the material for a post or reels stands.
type matState int

const (
	matReceived matState = iota
	matMissed            // asked for, not sent by the publication
	matWaiting
	matAhead
	matNotAsked
	matAsked // asked for; with the replies off, nothing more is known
	matStates
)

var matLabels = [matStates]stateLabel{
	matReceived: {"✅", "получен"},
	matMissed:   {"⚠️", "не прислали"},
	matWaiting:  {"⏳", "ждём"},
	matAhead:    {"🕓", "запросим"},
	matNotAsked: {"🔕", "не запрашивали"},
	matAsked:    {"📨", "попросили"},
}

// matStateOf says where the material stands; replies tells whether the bot
// takes it as a reply (FEATURE_MATERIAL_REPLIES). Without the replies it
// cannot know whether the material came, only whether it was asked for.
func matStateOf(task storage.Task, now time.Time, replies bool) matState {
	switch {
	case !replies && task.MaterialStep > 0:
		return matAsked
	case replies && !task.MaterialReceivedAt.IsZero():
		return matReceived
	case task.MaterialStep > 0 && !now.Before(task.PublishAt):
		return matMissed
	case task.MaterialStep > 0:
		return matWaiting
	case now.Sub(materialRequestTime(task.PublishAt.In(now.Location()))) <= materialRequestGrace:
		return matAhead
	}
	return matNotAsked
}

// tally renders the non-zero counts: "✅ 3 · ⚠️ 1", or named:
// "✅ выложено 3 · ⚠️ нет ответа 1".
func tally(counts []int, labels []stateLabel, named bool) string {
	var parts []string
	for i, n := range counts {
		if n == 0 {
			continue
		}
		if named {
			parts = append(parts, fmt.Sprintf("%s %s %d", labels[i].icon, labels[i].name, n))
		} else {
			parts = append(parts, fmt.Sprintf("%s %d", labels[i].icon, n))
		}
	}
	return strings.Join(parts, " · ")
}

// kindGroups are how the reports split the publications: Instagram stories,
// Instagram posts and reels, and Telegram.
type kindGroup struct {
	title string
	types []storage.TaskType
}

var kindGroups = []kindGroup{
	{"📱 Сторис", []storage.TaskType{storage.TaskStories}},
	{"🖼 Посты и рилсы", []storage.TaskType{storage.TaskPost, storage.TaskReels}},
	{"✈️ Telegram", []storage.TaskType{storage.TaskTelegram}},
}

type taskGroup struct {
	title string
	tasks []storage.Task
}

// groupByKind splits tasks by kindGroups, keeping their order; empty groups
// are left out. A type in no group (should YouGile get a new column) gets a
// group of its own at the end, so nothing is lost.
func groupByKind(tasks []storage.Task) []taskGroup {
	groups := slices.Clone(kindGroups)
	for _, task := range tasks {
		if !slices.ContainsFunc(groups, func(g kindGroup) bool { return slices.Contains(g.types, task.Type) }) {
			groups = append(groups, kindGroup{task.Type.Label(), []storage.TaskType{task.Type}})
		}
	}
	var out []taskGroup
	for _, g := range groups {
		var in []storage.Task
		for _, task := range tasks {
			if slices.Contains(g.types, task.Type) {
				in = append(in, task)
			}
		}
		if len(in) > 0 {
			out = append(out, taskGroup{g.title, in})
		}
	}
	return out
}

// todayText is the publications part of /today, from the tasks published
// within [today, today+tickAhead).
func todayText(tasks []storage.Task, names map[string]string, now time.Time, asks, replies bool) string {
	tomorrow := startOfDay(now).AddDate(0, 0, 1)
	var today, asked []storage.Task
	for _, task := range tasks {
		p := task.PublishAt.In(now.Location())
		switch {
		case p.Before(tomorrow):
			today = append(today, task)
		case task.Type.NeedsMaterial() && sameDay(materialRequestTime(p), now):
			asked = append(asked, task)
		}
	}

	var sb strings.Builder
	if len(today) == 0 {
		sb.WriteString("📌 Публикаций сегодня нет.\n")
	} else {
		var counts [pubStates]int
		for _, task := range today {
			counts[pubStateOf(task, now, asks)]++
		}
		fmt.Fprintf(&sb, "📌 <b>Публикации · %d</b>\n%s\n", len(today), tally(counts[:], pubLabels[:], true))
		// Each group in a quote: the bar on its left shows where one ends.
		for _, g := range groupByKind(today) {
			var items []string
			for _, task := range g.tasks {
				item := fmt.Sprintf("<b>%s</b> %s\n└ %s · %s", clock(task.PublishAt.In(now.Location())),
					taskTitle(task), whoOf(task, names), publishStatusText(task, now, asks))
				if task.Type.NeedsMaterial() {
					item += "\n└ 📎 материал: " + materialStatusText(task, now, replies)
				}
				items = append(items, item)
			}
			fmt.Fprintf(&sb, "\n<b>%s · %d</b>\n<blockquote>%s</blockquote>\n", g.title, len(items), strings.Join(items, "\n\n"))
		}
	}
	if len(asked) > 0 {
		sb.WriteString("\n📎 <b>Материал на согласование</b> · запрос сегодня\n")
		for _, task := range asked {
			fmt.Fprintf(&sb, "\n<b>%s</b> %s\n└ %s · %s\n", dayTime(task.PublishAt.In(now.Location())),
				taskTitle(task), whoOf(task, names), materialStatusText(task, now, replies))
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// weekText is /week: the publications of the week from Monday, what needs
// attention and the week day by day, and the sales reports (none when the poll is off,
// sales == nil and salesOn false).
func weekText(tasks []storage.Task, names map[string]string, sales []storage.SalesReport, now time.Time, yougile, asks, replies, salesOn bool) string {
	start := startOfWeek(now)
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 <b>Неделя %s – %s</b>\n<i>на %s, %s</i>\n\n",
		formatDay(start), formatDay(start.AddDate(0, 0, 6)), formatDay(now), clock(now))

	switch {
	case !yougile:
		sb.WriteString("📌 Публикации: YouGile не подключён.\n")
	case len(tasks) == 0:
		sb.WriteString("📌 Публикаций на этой неделе нет.\n")
	default:
		sb.WriteString(weekTasksText(tasks, names, now, start, asks, replies))
	}

	if salesOn {
		sb.WriteString("\n💼 <b>Отчёты продажников</b>\n")
		sb.WriteString(salesWeekText(sales))
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// weekTasksText is the publications part of /week: what needs attention,
// then the week day by day, one short line per publication.
func weekTasksText(tasks []storage.Task, names map[string]string, now, start time.Time, asks, replies bool) string {
	loc := now.Location()
	var sb strings.Builder

	var kinds []string
	for _, g := range groupByKind(tasks) {
		kinds = append(kinds, fmt.Sprintf("%s %d", g.title, len(g.tasks)))
	}
	fmt.Fprintf(&sb, "📌 <b>Публикации · %d</b>\n%s\n", len(tasks), strings.Join(kinds, " · "))
	if asks {
		var counts [pubStates]int
		for _, task := range tasks {
			counts[pubStateOf(task, now, asks)]++
		}
		sb.WriteString(tally(counts[:], pubLabels[:], true) + "\n")
	}

	// What needs attention: material not sent, and with the questions on,
	// publications not done or not answered for.
	var attention []string
	ref := func(task storage.Task) string {
		return fmt.Sprintf("%s %s «%s» · %s", dayTime(task.PublishAt.In(loc)), typeIcon(task.Type),
			html.EscapeString(shorten(task.Title, weekTitleLen)), whoOf(task, names))
	}
	noAssignee := 0
	for _, task := range tasks {
		if task.Type.NeedsMaterial() && matStateOf(task, now, replies) == matMissed {
			attention = append(attention, "• 📎 материал не прислали: "+ref(task))
		}
		if asks {
			switch pubStateOf(task, now, asks) {
			case pubNotDone:
				attention = append(attention, "• ❌ не выложено: "+ref(task))
			case pubNoAnswer:
				attention = append(attention, "• ⚠️ нет ответа «выложено?»: "+ref(task))
			}
		}
		if len(task.Assignees) == 0 {
			noAssignee++
		}
	}
	if noAssignee > 0 {
		attention = append(attention, fmt.Sprintf("• без исполнителя: %d — бот тегает основного админа", noAssignee))
	}
	if len(attention) > 0 {
		sb.WriteString("\n⚠️ <b>Внимание</b>\n" + strings.Join(attention, "\n") + "\n")
	}

	sb.WriteString("\n")
	var matSeen [matStates]bool
	for d := range 7 {
		day := start.AddDate(0, 0, d)
		var lines []string
		for _, task := range tasks {
			p := task.PublishAt.In(loc)
			if !sameDay(p, day) {
				continue
			}
			line := fmt.Sprintf("%s %s «%s» · %s", clock(p), typeIcon(task.Type),
				html.EscapeString(shorten(task.Title, weekTitleLen)), shortWho(task, names))
			if task.Type.NeedsMaterial() {
				st := matStateOf(task, now, replies)
				matSeen[st] = true
				line += " · 📎" + matLabels[st].icon
			}
			if asks {
				line += " · " + pubLabels[pubStateOf(task, now, asks)].icon
			}
			lines = append(lines, line)
		}
		label := formatDay(day)
		if sameDay(day, now) {
			label += " · сегодня"
		}
		if len(lines) == 0 {
			fmt.Fprintf(&sb, "%s — нет\n", label)
			continue
		}
		fmt.Fprintf(&sb, "<b>%s</b> · %d\n<blockquote>%s</blockquote>\n", label, len(lines), strings.Join(lines, "\n"))
	}
	// The material icons explained, those that occur.
	var legend []string
	for st, seen := range matSeen {
		if seen {
			legend = append(legend, matLabels[st].icon+" "+matLabels[st].name)
		}
	}
	if len(legend) > 0 {
		sb.WriteString("<i>📎 материал (посты и рилсы): " + strings.Join(legend, " · ") + "</i>\n")
	}
	return sb.String()
}

// weekTitleLen keeps a week of publications within one Telegram message.
const weekTitleLen = 40

// typeIcon is the icon of the type's Label: "📱".
func typeIcon(t storage.TaskType) string {
	l := t.Label()
	if i := strings.IndexByte(l, ' '); i > 0 {
		return l[:i]
	}
	return l
}

// shortWho is whoOf for the week's lines: "—" for no assignee.
func shortWho(task storage.Task, names map[string]string) string {
	if who := joinNames(task.Assignees, names); who != "" {
		return who
	}
	return "—"
}

var salesLabels = map[storage.SalesStatus]stateLabel{
	storage.SalesSent:     {"✅", "отправили"},
	storage.SalesNotSent:  {"❌", "не отправили"},
	storage.SalesNoAnswer: {"⚠️", "нет ответа"},
	storage.SalesPending:  {"⏳", "ждём"},
}

// salesWeekText lists each sales user's week: "@ivan — чт ✅ · пт ⚠️".
func salesWeekText(reports []storage.SalesReport) string {
	if len(reports) == 0 {
		return "Опросов на этой неделе не было.\n"
	}
	var order []int64
	users := map[int64]storage.User{}
	days := map[int64][]string{}
	var counts [4]int
	statuses := []storage.SalesStatus{storage.SalesSent, storage.SalesNotSent, storage.SalesNoAnswer, storage.SalesPending}
	for _, r := range reports {
		if _, seen := users[r.User.TgID]; !seen {
			order = append(order, r.User.TgID)
			users[r.User.TgID] = r.User
		}
		day := r.Date
		if d, err := time.Parse(time.DateOnly, r.Date); err == nil {
			day = weekdays[d.Weekday()]
		}
		days[r.User.TgID] = append(days[r.User.TgID], day+" "+salesLabels[r.Status].icon)
		counts[slices.Index(statuses, r.Status)]++
	}

	var sb strings.Builder
	for _, id := range order {
		sb.WriteString(displayName(users[id]) + " — " + strings.Join(days[id], " · ") + "\n")
	}
	labels := make([]stateLabel, len(statuses))
	for i, s := range statuses {
		labels[i] = salesLabels[s]
	}
	sb.WriteString(tally(counts[:], labels, true) + "\n")
	return sb.String()
}

// publishStatusText says where a publication stands, for /today.
func publishStatusText(task storage.Task, now time.Time, asks bool) string {
	p := task.PublishAt.In(now.Location())
	switch pubStateOf(task, now, asks) {
	case pubDone:
		if task.AnsweredAttempt > 0 {
			return fmt.Sprintf("✅ выложено, ответ с %d-й попытки", task.AnsweredAttempt)
		}
		return "✅ выложено"
	case pubNotDone:
		return "❌ не выложено"
	case pubNoAnswer:
		return fmt.Sprintf("⚠️ нет ответа на %d %s", task.Attempts, plural(task.Attempts, "вопрос", "вопроса", "вопросов"))
	case pubWaiting:
		if task.Attempts > 0 {
			return fmt.Sprintf("⏳ ждём ответа · спросил %d из %d", task.Attempts, publishAttempts)
		}
		if ask := outsideQuietHours(p.Add(taskStep)); now.Before(ask) {
			return "🕓 спрошу «выложено?» в " + clock(ask)
		}
		return "⏳ сейчас спрошу «выложено?»"
	case pubPassed:
		return "🕘 время публикации прошло"
	case pubAhead:
		if task.RemindersSent > 0 {
			return "🔔 напомнил"
		}
		return "🕓 ещё не время"
	}
	return "🔕 не спрашивали"
}

// materialStatusText says where the material stands, for /today.
func materialStatusText(task storage.Task, now time.Time, replies bool) string {
	switch matStateOf(task, now, replies) {
	case matAsked:
		if task.MaterialStep >= materialLastStep {
			return "📨 попросил · напомнил"
		}
		return "📨 попросил · напомню через час"
	case matReceived:
		return "✅ получен"
	case matMissed:
		return "⚠️ не прислали"
	case matWaiting:
		if task.MaterialStep >= materialLastStep {
			return "⏳ ждём · напомнил"
		}
		return "⏳ ждём · напомню через час после запроса"
	case matAhead:
		if r := materialRequestTime(task.PublishAt.In(now.Location())); now.Before(r) {
			return "🕓 запросим " + whenText(r, now)
		}
		return "⏳ запрашиваю"
	}
	return "🔕 не запрашивали"
}

func taskTitle(task storage.Task) string {
	return task.Type.Label() + " «" + html.EscapeString(task.Title) + "»"
}

func whoOf(task storage.Task, names map[string]string) string {
	if who := joinNames(task.Assignees, names); who != "" {
		return who
	}
	return "без исполнителя"
}

// tasksWithNames loads the active tasks published within [from, to) and the
// names of their assignees.
func (t *Bot) tasksWithNames(ctx context.Context, from, to time.Time) ([]storage.Task, map[string]string, error) {
	tasks, err := t.store.ActiveTasks(ctx, from, to)
	if err != nil {
		return nil, nil, err
	}
	var ids []string
	for _, task := range tasks {
		ids = append(ids, task.Assignees...)
	}
	names, err := t.store.YouGileNames(ctx, ids)
	return tasks, names, err
}

// tasksTodayText is the publications part of /today.
func (t *Bot) tasksTodayText(ctx context.Context) (string, error) {
	if t.yg == nil {
		return "📌 Публикации: YouGile не подключён.", nil
	}
	now := time.Now().In(t.tz)
	day := startOfDay(now)
	tasks, names, err := t.tasksWithNames(ctx, day, day.Add(tickAhead))
	if err != nil {
		return "", err
	}
	return todayText(tasks, names, now, t.features.PublishQuestions, t.features.MaterialReplies), nil
}

// weekReport is /week.
func (t *Bot) weekReport(ctx context.Context) (string, error) {
	now := time.Now().In(t.tz)
	start := startOfWeek(now)
	var tasks []storage.Task
	var names map[string]string
	if t.yg != nil {
		var err error
		if tasks, names, err = t.tasksWithNames(ctx, start, start.AddDate(0, 0, 7)); err != nil {
			return "", err
		}
	}
	var sales []storage.SalesReport
	if t.features.SalesPoll {
		var err error
		sales, err = t.store.SalesReports(ctx, storage.SalesDate(start), storage.SalesDate(start.AddDate(0, 0, 6)))
		if err != nil {
			return "", err
		}
	}
	return weekText(tasks, names, sales, now, t.yg != nil, t.features.PublishQuestions, t.features.MaterialReplies, t.features.SalesPoll), nil
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// startOfWeek is the start of the week's Monday.
func startOfWeek(t time.Time) time.Time {
	day := startOfDay(t)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}

// daysBetween counts calendar days from the day of a to the day of b.
func daysBetween(a, b time.Time) int {
	return int(startOfDay(b).Sub(startOfDay(a)).Round(24*time.Hour) / (24 * time.Hour))
}

func sameDay(a, b time.Time) bool {
	return daysBetween(a, b.In(a.Location())) == 0
}

// plural picks the Russian form for n: plural(n, "минуту", "минуты", "минут").
func plural(n int, one, few, many string) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return few
	}
	return many
}
