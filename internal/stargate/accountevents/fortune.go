package accountevents

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"src.solsynth.dev/sosys/stargate/model"
)

// Fortune tips and the fallback fortune report. Passport generates the report
// through the MiChan personality service; Stargate has no personality client,
// so the deterministic fallback is always used. The tip catalogue is copied
// from Passport's en.json / zh-hans.json locale files.

const fortuneTipCount = 14

type tipText struct {
	Title   string
	Content string
}

type fortuneLocale struct {
	SpecialTitle   string
	SpecialContent string
	Positive       [fortuneTipCount]tipText
	Negative       [fortuneTipCount]tipText
}

var englishFortune = fortuneLocale{
	SpecialTitle:   "Have a Birthday Party",
	SpecialContent: "Happy Birthday, {user}!",
	Positive: [fortuneTipCount]tipText{
		{"Gacha", "Comes at your first roll"},
		{"Gaming", "Ranking up as god"},
		{"Lottery", "Never miss a first"},
		{"Speech", "Flowing at precision"},
		{"Drawing", "Expectation is envisioned"},
		{"Coding", "0 error(s), 0 warning(s)"},
		{"Shopping", "No taxes and extra fees"},
		{"Studying", "Efficiency X"},
		{"Music composing", "No need to re-listen"},
		{"Imaging", "Perfect every shot"},
		{"PCB welding", "Solders, fluxes, GO!"},
		{"After Effects", "@ 60 fps"},
		{"Drone shot", "Ready to Go"},
		{"Color grading", "In this format, at this color"},
	},
	Negative: [fortuneTipCount]tipText{
		{"Gacha", "Won't get at 80, but 200"},
		{"Gaming", "Ground's limit"},
		{"Lottery", "Zero in multiple ten"},
		{"Speech", "Be careful what you say"},
		{"Drawing", "Your pen is sticky"},
		{"Coding", "114 error(s), 514 warning(s)"},
		{"Shopping", "245% tariff"},
		{"Studying", "Studying Fatigue III"},
		{"Music composing", "FL Studio is not responding"},
		{"Imaging", "Card cannot be accessed"},
		{"PCB welding", "Hand welding"},
		{"After Effects", "Baaah!"},
		{"Drone shot", "Low battery level, Auto Landing in 16s"},
		{"Color grading", "Crummy screen now"},
	},
}

var simplifiedChineseFortune = fortuneLocale{
	SpecialTitle:   "举办生日派对",
	SpecialContent: "生日快乐，{user}！",
	Positive: [fortuneTipCount]tipText{
		{"抽卡", "一抽就中"},
		{"游戏", "封神"},
		{"抽奖", "绝不错过首抽"},
		{"配音", "行云流水"},
		{"作画", "预期被实现"},
		{"编程", "0个错误，0个警告"},
		{"购物", "无税，无额外费用"},
		{"学习", "效率X"},
		{"音乐创作", "无需重新上线"},
		{"图像处理", "每张都很完美"},
		{"手工焊接", "焊工、焊料、起飞！"},
		{"后期", "@ 60帧"},
		{"云台", "准备出发"},
		{"调色", "在此格式，进行调色"},
	},
	Negative: [fortuneTipCount]tipText{
		{"抽卡", "保底80却出200"},
		{"游戏", "地板"},
		{"抽奖", "十连没出"},
		{"配音", "慎言"},
		{"作画", "笔尖不顺畅"},
		{"编程", "114个错误，514个警告"},
		{"购物", "245%关税"},
		{"学习", "学习疲劳III"},
		{"音乐创作", "FL Studio没有响应"},
		{"图像处理", "无法读取SD卡"},
		{"手工焊接", "手工焊接"},
		{"后期", "啊啊啊！"},
		{"云台", "电量低，自动降落16秒"},
		{"调色", "屏幕现在很烂"},
	},
}

// fortuneLocaleFor selects the locale the way the C# localizer does
// (zh* -> zh-hans, everything else -> en).
func fortuneLocaleFor(language string) fortuneLocale {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh") {
		return simplifiedChineseFortune
	}
	return englishFortune
}

// buildFortuneTips mirrors the C# tip generation: a birthday gets a single
// special tip, otherwise two distinct positive and two distinct negative tips
// drawn from the 14-entry catalogue.
func buildFortuneTips(account *model.Account, isBirthday bool) []FortuneTip {
	locale := fortuneLocaleFor(account.Language)
	if isBirthday {
		nick := account.Nick
		if strings.TrimSpace(nick) == "" {
			nick = account.Name
		}
		return []FortuneTip{{
			IsPositive: true,
			Title:      locale.SpecialTitle,
			Content:    strings.ReplaceAll(locale.SpecialContent, "{user}", nick),
		}}
	}

	positiveIndices := rand.Perm(fortuneTipCount)[:2]
	negativeCandidates := make([]int, 0, fortuneTipCount-2)
	for _, index := range rand.Perm(fortuneTipCount) {
		if index != positiveIndices[0] && index != positiveIndices[1] {
			negativeCandidates = append(negativeCandidates, index)
		}
	}
	if len(negativeCandidates) < 2 {
		// Only possible with a degenerate catalogue; keep output well-formed.
		negativeCandidates = append(negativeCandidates, positiveIndices...)
	}

	tips := make([]FortuneTip, 0, 4)
	for _, index := range positiveIndices {
		tip := locale.Positive[index]
		tips = append(tips, FortuneTip{IsPositive: true, Title: tip.Title, Content: tip.Content})
	}
	for _, index := range negativeCandidates[:2] {
		tip := locale.Negative[index]
		tips = append(tips, FortuneTip{IsPositive: false, Title: tip.Title, Content: tip.Content})
	}
	return tips
}

// rollCheckInLevel mirrors the weighted distribution: 10/20/40/20/10.
func rollCheckInLevel() CheckInLevel {
	switch value := rand.IntN(100); {
	case value < 10:
		return CheckInWorst
	case value < 30:
		return CheckInWorse
	case value < 70:
		return CheckInNormal
	case value < 90:
		return CheckInBetter
	default:
		return CheckInBest
	}
}

// randPercent returns a random integer in [0, 100).
func randPercent() int { return rand.IntN(100) }

// fallbackFortuneReport mirrors CreateFallbackFortuneReport.
func fallbackFortuneReport(account *model.Account, isBirthday bool, level CheckInLevel, tips []FortuneTip) *FortuneReport {
	var positiveTip *FortuneTip
	var negativeTip *FortuneTip
	for i := range tips {
		if tips[i].IsPositive && positiveTip == nil {
			positiveTip = &tips[i]
		}
		if !tips[i].IsPositive && negativeTip == nil {
			negativeTip = &tips[i]
		}
	}
	if positiveTip == nil && len(tips) > 0 {
		positiveTip = &tips[0]
	}

	nick := account.Nick
	if strings.TrimSpace(nick) == "" {
		nick = account.Name
	}
	tone := "签影平和，宜顺势而行"
	switch level {
	case CheckInWorst:
		tone = "云色稍沉，宜慢行守心"
	case CheckInWorse:
		tone = "风声偏紧，宜稳住节奏"
	case CheckInBetter:
		tone = "铃音渐明，前路有微光"
	case CheckInBest:
		tone = "晴光入签，所行多得助"
	case CheckInSpecial:
		tone = "星灯为你而明，今日自有祝福"
	}
	positiveContent := "顺手完成的小事会带来一点确定感。"
	if positiveTip != nil {
		positiveContent = positiveTip.Content
	}
	summaryExtra := "把心放稳，小事也会慢慢顺起来。"
	if positiveTip != nil {
		summaryExtra = positiveTip.Content
	}
	wish := "愿望宜从一个小动作开始。"
	if positiveTip != nil {
		wish = positiveTip.Title
	}
	lostItem := "先看常用包袋和桌角附近。"
	if negativeTip != nil {
		lostItem = "失物多在顺手放下之处，慢慢回想路径。"
	}

	poem := "风过签筒静，铃响见微光。"
	luckyColor := "浅青色"
	if isBirthday {
		poem = "星灯落掌心，花影绕今日。"
		luckyColor = "星白色"
	}

	return &FortuneReport{
		Version:        2,
		Poem:           poem,
		Summary:        fmt.Sprintf("%s，今日%s。%s", nick, tone, summaryExtra),
		SummaryDetail:  fmt.Sprintf("%s，今日建议你先把节奏放稳，再选择最值得推进的一件事。%s 若遇到卡顿，不必急着证明自己，先整理线索、减少分心；把注意力放在能立即收尾的小行动上，会比反复犹豫更有帮助。", nick, positiveContent),
		Wish:           wish,
		Love:           "温柔表达比反复猜测更有力量。",
		Study:          "适合整理旧知识，细节里会有新线索。",
		Career:         "先稳住手边事务，再推进新的判断。",
		Health:         "留意休息和饮水，不必把自己逼得太紧。",
		LostItem:       lostItem,
		LuckyColor:     luckyColor,
		LuckyDirection: "东南",
		LuckyTime:      "午后",
		LuckyItem:      "随身钥匙",
		LuckyAction:    "把一件拖延的小事收尾。",
		AvoidAction:    "避免在情绪上头时立刻做决定。",
		Ritual:         "出门前整理桌面一角，给今天留出清爽的开端。",
	}
}

// completeFortuneReport fills every empty report field from the fallback,
// mirroring CompleteFortuneReport.
func completeFortuneReport(account *model.Account, result *CheckInResult) *FortuneReport {
	fallback := fallbackFortuneReport(account, result.Level == CheckInSpecial, result.Level, result.Tips)
	report := result.FortuneReport
	if report == nil {
		return fallback
	}
	report.Version = 2
	report.Poem = pickFortuneText(report.Poem, fallback.Poem)
	report.Summary = pickFortuneText(report.Summary, fallback.Summary)
	report.SummaryDetail = pickFortuneText(report.SummaryDetail, fallback.SummaryDetail)
	report.Wish = pickFortuneText(report.Wish, fallback.Wish)
	report.Love = pickFortuneText(report.Love, fallback.Love)
	report.Study = pickFortuneText(report.Study, fallback.Study)
	report.Career = pickFortuneText(report.Career, fallback.Career)
	report.Health = pickFortuneText(report.Health, fallback.Health)
	report.LostItem = pickFortuneText(report.LostItem, fallback.LostItem)
	report.LuckyColor = pickFortuneText(report.LuckyColor, fallback.LuckyColor)
	report.LuckyDirection = pickFortuneText(report.LuckyDirection, fallback.LuckyDirection)
	report.LuckyTime = pickFortuneText(report.LuckyTime, fallback.LuckyTime)
	report.LuckyItem = pickFortuneText(report.LuckyItem, fallback.LuckyItem)
	report.LuckyAction = pickFortuneText(report.LuckyAction, fallback.LuckyAction)
	report.AvoidAction = pickFortuneText(report.AvoidAction, fallback.AvoidAction)
	report.Ritual = pickFortuneText(report.Ritual, fallback.Ritual)
	return report
}

func pickFortuneText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
