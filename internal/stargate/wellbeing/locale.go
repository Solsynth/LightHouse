package wellbeing

import "strings"

// NightOwlCopy is one reminder variant's localized title and body.
type NightOwlCopy struct {
	Title string
	Body  string
}

// The tables mirror Passport's nightOwlReminder{0,1,2}{Title,Body}{1,2,3}
// locale keys verbatim: hours 0..2, variants 1..3. Chinese keys are the
// zh-hans values; other zh-* languages select the same copy.
var nightOwlEnglish = [3][3]NightOwlCopy{
	{
		{
			Title: "A sleepy little reminder (˘ω˘)",
			Body:  "The moon says it’s bedtime—tuck yourself in and get some cozy rest, okay?",
		},
		{
			Title: "Your bed misses you (￣o￣) zzZZ",
			Body:  "It’s getting late. Go give your blanket a big cozy hug!",
		},
		{
			Title: "Psst… sleepy time (｡- .-｡)",
			Body:  "Your pillow is waiting patiently. Sweet dreams, little night owl!",
		},
	},
	{
		{
			Title: "Still awake? (・_・;)",
			Body:  "It’s one o’clock already. Let’s wrap things up and get some sleep soon.",
		},
		{
			Title: "Bedtime is calling again (¬_¬)",
			Body:  "Your future self would really appreciate a little rest right now.",
		},
		{
			Title: "One more thing… then bed! (ง •̀_•́)ง",
			Body:  "Promise? Finish up gently and let today come to a close.",
		},
	},
	{
		{
			Title: "Hey, little night owl! (╬ Ò﹏Ó)",
			Body:  "It’s two in the morning! Go to sleep now, or Solar Network is off-limits tomorrow! W(`0`)W",
		},
		{
			Title: "This is your final bedtime notice! (╯°□°)╯",
			Body:  "No more scrolling—your blanket needs you immediately. Shoo, shoo!",
		},
		{
			Title: "Two AM?! Absolutely not! (＞﹏＜)",
			Body:  "Close the app and sleep, please. Tomorrow-you deserves a well-rested brain!",
		},
	},
}

var nightOwlChinese = [3][3]NightOwlCopy{
	{
		{
			Title: "小月亮来提醒你啦 (˘ω˘)",
			Body:  "夜已经很深啦，该钻进暖暖的被窝休息咯～",
		},
		{
			Title: "被窝在想你啦 (￣o￣) zzZZ",
			Body:  "今天已经很努力了，快去抱抱软软的枕头吧！",
		},
		{
			Title: "嘘……该睡觉啦 (｡- .-｡)",
			Body:  "枕头已经等你好久了，晚安呀，小夜猫！",
		},
	},
	{
		{
			Title: "还没睡呀？(・_・;)",
			Body:  "已经一点钟啦，差不多该收尾，准备睡觉咯。",
		},
		{
			Title: "被窝又来催你啦 (¬_¬)",
			Body:  "明天的你会很感谢现在愿意早点休息的你哦。",
		},
		{
			Title: "再做一件事……就睡！(ง •̀_•́)ง",
			Body:  "说好了哦？慢慢收尾，让今天好好结束吧。",
		},
	},
	{
		{
			Title: "小夜猫，不可以啦！(╬ Ò﹏Ó)",
			Body:  "都两点了，快去睡觉！再不睡明天一天不准用 Solar Network！W(`0`)W",
		},
		{
			Title: "这是最后一张睡觉通知！(╯°□°)╯",
			Body:  "不许再刷啦，快去找你的被窝报到！快快快～",
		},
		{
			Title: "两点了？！不行不行！(＞﹏＜)",
			Body:  "快关掉 App 睡觉啦。明天的你值得一个睡饱饱的脑袋！",
		},
	},
}

// LookupNightOwl selects the copy for a local hour (0..2) and variant (1..3).
// Unknown languages fall back to English; zh-* languages use zh-hans.
func LookupNightOwl(language string, hour, variant int) NightOwlCopy {
	if hour < 0 || hour > 2 {
		hour = 0
	}
	if variant < 1 || variant > 3 {
		variant = 1
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh") {
		return nightOwlChinese[hour][variant-1]
	}
	return nightOwlEnglish[hour][variant-1]
}
