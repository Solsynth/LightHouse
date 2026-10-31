package accounteventsctl

import (
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/go/pkg/errs"
)

// fortuneSaying mirrors FortuneSaying (content, source, language).
type fortuneSaying struct {
	Content  string `json:"content"`
	Source   string `json:"source"`
	Language string `json:"language"`
}

// fortuneSayings is Passport's FortuneSayingController catalogue, copied
// verbatim.
var fortuneSayings = []fortuneSaying{
	// Chinese sayings
	{"天行健，君子以自强不息。", "周易", "zh"},
	{"地势坤，君子以厚德载物。", "周易", "zh"},
	{"天道酬勤。", "谚语", "zh"},
	{"天时不如地利，地利不如人和。", "孟子", "zh"},
	{"君子有三畏：畏天命，畏大人，畏圣人之言。", "论语", "zh"},
	{"己所不欲，勿施于人。", "孔子", "zh"},
	{"学而时习之，不亦说乎？", "论语", "zh"},
	{"有朋自远方来，不亦乐乎？", "论语", "zh"},
	{"人不知而不愠，不亦君子乎？", "论语", "zh"},
	{"不愤不启，不悱不发。", "论语", "zh"},
	{"温故而知新，可以为师矣。", "论语", "zh"},
	{"见贤思齐焉，见不贤而内自省也。", "论语", "zh"},
	{"君子坦荡荡，小人长戚戚。", "论语", "zh"},
	{"道不远人。人之为道而远人，不可以为道。", "论语", "zh"},
	{"巧言令色，鲜矣仁。", "论语", "zh"},
	{"岁寒，然后知松柏之后凋也。", "孔子", "zh"},
	{"志于道，据于德，依于仁，游于艺。", "论语", "zh"},
	{"博学而笃志，切问而近思。", "论语", "zh"},
	{"知之者不如好之者，好之者不如乐之者。", "论语", "zh"},
	{"君子欲讷于言而敏于行。", "论语", "zh"},
	{"君子周而不比，小人比而不周。", "论语", "zh"},
	{"君子喻于义，小人喻于利。", "论语", "zh"},
	{"君子怀德，小人怀土。", "论语", "zh"},
	{"君子矜而不争，群而不党。", "论语", "zh"},
	{"君子和而不同，小人同而不和。", "论语", "zh"},
	{"君子泰而不骄，小人骄而不泰。", "论语", "zh"},
	{"君子谋道不谋食。", "论语", "zh"},
	{"君子食无求饱，居无求安。", "论语", "zh"},
	{"君子学以致其道。", "论语", "zh"},
	{"君子耻其言而过其行。", "论语", "zh"},
	{"君子敬而无失，与人恭而有礼。", "论语", "zh"},
	{"君子求诸己，小人求诸人。", "论语", "zh"},
	{"君子慎独。", "论语", "zh"},
	{"君子不以言举人，不以人废言。", "论语", "zh"},
	{"君子不器。", "论语", "zh"},
	{"君子有终身之忧，无一朝之患。", "论语", "zh"},
	{"君子固穷，小人穷斯滥矣。", "论语", "zh"},
	{"君子疾没世而名不称焉。", "论语", "zh"},
	{"君子而不仁者有矣夫，未有小人而仁者也。", "论语", "zh"},
	{"君子义以为质，礼以行之，逊以出之，信以成之。", "论语", "zh"},
	{"君子之德风，小人之德草。", "论语", "zh"},
	{"君子之过也，如日月之食焉。", "论语", "zh"},
	{"君子之言，寡而实；小人之言，多而虚。", "论语", "zh"},
	{"君子之行，静以修身；小人之行，躁以求名。", "论语", "zh"},
	{"君子之交淡若水，小人之交甘若醴。", "论语", "zh"},
	{"君子之泽，五世而斩；小人之泽，亦五世而斩。", "孟子", "zh"},
	{"君子有三乐，而王天下不与存焉。", "孟子", "zh"},
	{"君子有三戒：少之时，血气未定，戒之在色；及其壮也，血气方刚，戒之在斗；及其老也，血气既衰，戒之在得。", "论语", "zh"},
	{"君子莫大乎与人为善。", "孟子", "zh"},
	{"君子远庖厨。", "孟子", "zh"},
	// English sayings
	{"The only way to do great work is to love what you do.", "Steve Jobs", "en"},
	{"Believe you can and you're halfway there.", "Theodore Roosevelt", "en"},
	{"The future belongs to those who believe in the beauty of their dreams.", "Eleanor Roosevelt", "en"},
	{"You miss 100% of the shots you don't take.", "Wayne Gretzky", "en"},
	{"The best way to predict the future is to create it.", "Peter Drucker", "en"},
	{"Fortune favors the bold.", "Virgil", "en"},
	{"Luck is what happens when preparation meets opportunity.", "Seneca", "en"},
	{"The harder you work, the luckier you get.", "Gary Player", "en"},
	{"Success is not final, failure is not fatal: It is the courage to continue that counts.", "Winston Churchill", "en"},
	{"The pessimist complains about the wind; the optimist expects it to change; the realist adjusts the sails.", "William Arthur Ward", "en"},
	{"The road to success is dotted with many tempting parking spaces.", "Will Rogers", "en"},
	{"Don't watch the clock; do what it does. Keep going.", "Sam Levenson", "en"},
	{"The only limit to our realization of tomorrow will be our doubts of today.", "Franklin D. Roosevelt", "en"},
	{"Your time is limited, so don't waste it living someone else's life.", "Steve Jobs", "en"},
	{"The way to get started is to quit talking and begin doing.", "Walt Disney", "en"},
	{"If you look at what you have in life, you'll always have more.", "Oprah Winfrey", "en"},
	{"The best revenge is massive success.", "Frank Sinatra", "en"},
	{"You must do the things you think you cannot do.", "Eleanor Roosevelt", "en"},
	{"Keep your face always toward the sunshine—and shadows will fall behind you.", "Walt Whitman", "en"},
	{"The greatest glory in living lies not in never falling, but in rising every time we fall.", "Nelson Mandela", "en"},
	{"Life is what happens to you while you're busy making other plans.", "John Lennon", "en"},
	{"The secret of getting ahead is getting started.", "Mark Twain", "en"},
	{"Believe in yourself and all that you are.", "Christian D. Larson", "en"},
	{"The only person you are destined to become is the person you decide to be.", "Ralph Waldo Emerson", "en"},
	{"Dream big and dare to fail.", "Norman Vaughan", "en"},
	{"What lies behind us and what lies before us are tiny matters compared to what lies within us.", "Ralph Waldo Emerson", "en"},
	{"You can't use up creativity. The more you use, the more you have.", "Maya Angelou", "en"},
	{"The mind is everything. What you think you become.", "Buddha", "en"},
	{"The best time to plant a tree was 20 years ago. The second best time is now.", "Chinese Proverb", "en"},
	{"Fall seven times, stand up eight.", "Japanese Proverb", "en"},
	{"The journey of a thousand miles begins with a single step.", "Lao Tzu", "en"},
	{"Be not afraid of growing slowly, be afraid only of standing still.", "Chinese Proverb", "en"},
	{"A bird does not sing because it has an answer. It sings because it has a song.", "Chinese Proverb", "en"},
	{"Do not dwell in the past, do not dream of the future, concentrate the mind on the present moment.", "Buddha", "en"},
	{"The best and most beautiful things in the world cannot be seen or even touched - they must be felt with the heart.", "Helen Keller", "en"},
	{"Keep your eyes on the stars, and your feet on the ground.", "Theodore Roosevelt", "en"},
	{"The only true wisdom is in knowing you know nothing.", "Socrates", "en"},
	{"In the middle of every difficulty lies opportunity.", "Albert Einstein", "en"},
	{"What you get by achieving your goals is not as important as what you become by achieving your goals.", "Zig Ziglar", "en"},
	{"The purpose of life is a life of purpose.", "Robert Byrne", "en"},
	{"You become what you believe.", "Oprah Winfrey", "en"},
	{"The difference between a successful person and others is not a lack of strength, not a lack of knowledge, but rather a lack in will.", "Vince Lombardi", "en"},
	{"The only way to make sense out of change is to plunge into it, move with it, and join the dance.", "Alan Watts", "en"},
	{"Your work is going to fill a large part of your life, and the only way to be truly satisfied is to do what you believe is great work.", "Steve Jobs", "en"},
	{"The man who has confidence in himself gains the confidence of others.", "Hasidic Proverb", "en"},
	{"Courage is not the absence of fear, but rather the assessment that something else is more important than fear.", "Franklin D. Roosevelt", "en"},
	{"The best preparation for tomorrow is doing your best today.", "H. Jackson Brown Jr.", "en"},
	{"Believe in the power of your own voice. The more you use it, the stronger it becomes.", "Unknown", "en"},
	{"Opportunities don't happen, you create them.", "Chris Grosser", "en"},
}

// listFortunes mirrors FortuneSayingController.ListFortunes (anonymous in
// Passport; no auth middleware).
func (d Deps) listFortunes(c *gin.Context) {
	c.JSON(http.StatusOK, fortuneSayings)
}

// randomFortune mirrors FortuneSayingController.GetRandomFortunes.
func (d Deps) randomFortune(c *gin.Context) {
	language := strings.TrimSpace(c.Query("language"))
	filtered := fortuneSayings
	if language != "" {
		filtered = make([]fortuneSaying, 0, len(fortuneSayings))
		for _, saying := range fortuneSayings {
			if strings.EqualFold(saying.Language, language) {
				filtered = append(filtered, saying)
			}
		}
	}
	if len(filtered) == 0 {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_FORTUNE_NOT_FOUND",
			"No fortunes found for the specified language.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, []fortuneSaying{filtered[rand.IntN(len(filtered))]})
}
