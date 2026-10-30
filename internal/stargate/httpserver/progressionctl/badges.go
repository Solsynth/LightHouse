package progressionctl

// badgeEntry is one row of the C# BadgesManifestData.BadgeEntries table.
type badgeEntry struct {
	Identifier      string
	Achievement     string
	Label           string
	Caption         string
	Icon            string
	Color           string
	LocalizationKey string
	Category        string
	Series          map[string]any
	Hidden          bool
}

// badgeEntries is the badge discovery manifest, transliterated from
// Passport's BadgesDiscoveryController.cs.
var badgeEntries = []badgeEntry{
	{Identifier: "progression.post.expert", Achievement: "expert-post", Label: "Better than 陆游", Caption: "Created over 9362 posts", Icon: "draw", Color: "#6366f1", LocalizationKey: "badge.post_expert", Category: "post", Series: nil, Hidden: false},
	{Identifier: "progression.post.featured", Achievement: "initial-featured-post", Label: "Editor's Pick", Caption: "Created a post that got featured.", Icon: "recommend", Color: "#f59e0b", LocalizationKey: "badge.post_featured", Category: "post", Series: map[string]any{"identifier": "featured-post", "title": "Featured Posts", "order": 1}, Hidden: false},
	{Identifier: "progression.post.streak.30", Achievement: "streak-post-30", Label: "Serial Publisher", Caption: "Created posts for 30 days in a row.", Icon: "calendar-add-on", Color: "#f97316", LocalizationKey: "badge.post_streak_30", Category: "streak", Series: map[string]any{"identifier": "post-streak", "title": "Posting Streak", "order": 3}, Hidden: false},
	{Identifier: "progression.post.streak.90", Achievement: "streak-post-90", Label: "Quarterly Author", Caption: "Created posts for 90 days in a row.", Icon: "draw", Color: "#ea580c", LocalizationKey: "badge.post_streak_90", Category: "streak", Series: map[string]any{"identifier": "post-streak", "title": "Posting Streak", "order": 4}, Hidden: false},
	{Identifier: "progression.post.streak.365", Achievement: "streak-post-365", Label: "Daily Devotee", Caption: "Created posts for 365 days in a row.", Icon: "calendar-add-on", Color: "#dc2626", LocalizationKey: "badge.post_streak_365", Category: "streak", Series: map[string]any{"identifier": "post-streak", "title": "Posting Streak", "order": 5}, Hidden: false},
	{Identifier: "progression.login.streak.365", Achievement: "streak-login-365", Label: "Still Here", Caption: "Logged in for 365 days in a row.", Icon: "calendar-add-on", Color: "#eab308", LocalizationKey: "badge.login_streak_365", Category: "streak", Series: map[string]any{"identifier": "activity-streak", "title": "Activity Streak", "order": 3}, Hidden: false},
	{Identifier: "progression.login.streak.90", Achievement: "streak-login-90", Label: "Perennial", Caption: "Stayed active for 90 days in a row.", Icon: "calendar-add-on", Color: "#facc15", LocalizationKey: "badge.login_streak_90", Category: "streak", Series: map[string]any{"identifier": "activity-streak", "title": "Activity Streak", "order": 4}, Hidden: false},
	{Identifier: "progression.post.featured.expert", Achievement: "expert-featured-post", Label: "Hall of Fame", Caption: "Created 100 featured posts.", Icon: "recommend", Color: "#d97706", LocalizationKey: "badge.post_featured_expert", Category: "post", Series: map[string]any{"identifier": "featured-post", "title": "Featured Posts", "order": 2}, Hidden: false},
	{Identifier: "progression.stellar.supporter.12", Achievement: "stellar-supporter-12", Label: "Mega Supporter", Caption: "Purchased 12 eligible months of the Stellar Program.", Icon: "volunteer-activism", Color: "#a855f7", LocalizationKey: "badge.stellar_supporter_12", Category: "supporter", Series: map[string]any{"identifier": "stellar-supporter", "title": "Stellar Supporter", "order": 5}, Hidden: false},
	{Identifier: "progression.chat.expert", Achievement: "expert-chat", Label: "No-life's Otaku", Caption: "I can't believe some one spend for 10k minutes chatting on the Solar Network.", Icon: "do-not-step", Color: "#8b5cf6", LocalizationKey: "badge.chat_expert", Category: "chat", Series: nil, Hidden: false},
	{Identifier: "progression.account.avatar", Achievement: "first-avatar", Label: "Picture Perfect", Caption: "Set a profile picture for the first time.", Icon: "face", Color: "#22c55e", LocalizationKey: "badge.account_avatar", Category: "account", Series: nil, Hidden: false},
	{Identifier: "progression.friends.50", Achievement: "friends-50", Label: "Community Pillar", Caption: "Made 50 friends on the Solar Network.", Icon: "diversity-2", Color: "#ec4899", LocalizationKey: "badge.friends_50", Category: "social", Series: map[string]any{"identifier": "friend-count", "title": "Friend Count", "order": 4}, Hidden: false},
	{Identifier: "progression.friends.100", Achievement: "friends-100", Label: "The Connector", Caption: "Made 100 friends on the Solar Network.", Icon: "diversity-2", Color: "#f43f5e", LocalizationKey: "badge.friends_100", Category: "social", Series: map[string]any{"identifier": "friend-count", "title": "Friend Count", "order": 5}, Hidden: false},
	{Identifier: "progression.post.topical.first", Achievement: "first-topical-post", Label: "Well Structured", Caption: "Published a post with both categories and tags.", Icon: "shapes", Color: "#14b8a6", LocalizationKey: "badge.post_topical_first", Category: "post", Series: nil, Hidden: false},
	{Identifier: "progression.post.topical.50", Achievement: "topical-post-50", Label: "Organizer", Caption: "Published 50 posts with both categories and tags.", Icon: "shapes", Color: "#0d9488", LocalizationKey: "badge.post_topical_50", Category: "post", Series: nil, Hidden: false},
	{Identifier: "progression.reaction.expert", Achievement: "expert-reaction", Label: "Empath", Caption: "Reacted to 1000 posts on the Solar Network.", Icon: "bolt-boost", Color: "#e11d48", LocalizationKey: "badge.reaction_expert", Category: "social", Series: nil, Hidden: false},
	{Identifier: "progression.post.downvote.5", Achievement: "gots-fired-5", Label: "炎上", Caption: "Received repeated downvotes on a publisher post in one day.", Icon: "whatshot", Color: "#7c2d12", LocalizationKey: "badge.post_downvote_5", Category: "hidden", Series: nil, Hidden: true},
	{Identifier: "progression.post.downvote.20", Achievement: "gots-fired-20", Label: "炎上", Caption: "Received a lot of downvotes on publisher posts.", Icon: "whatshot", Color: "#991b1b", LocalizationKey: "badge.post_downvote_20", Category: "hidden", Series: nil, Hidden: true},
	{Identifier: "progression.account.profile_complete", Achievement: "profile-complete", Label: "Fully Realized", Caption: "Completed their profile.", Icon: "badge", Color: "#16a34a", LocalizationKey: "badge.account_profile_complete", Category: "account", Series: nil, Hidden: false},
	{Identifier: "progression.boost.50", Achievement: "boost-50", Label: "Signal Tower", Caption: "Boosted 50 posts on the Solar Network.", Icon: "bolt-boost", Color: "#0ea5e9", LocalizationKey: "badge.boost_50", Category: "post", Series: nil, Hidden: false},
	{Identifier: "progression.hidden.night_owl", Achievement: "hidden-night-owl", Label: "Night Owl", Caption: "Posted between midnight and 4am.", Icon: "moon-stars", Color: "#1e1b4b", LocalizationKey: "badge.hidden_night_owl", Category: "hidden", Series: nil, Hidden: true},
	{Identifier: "progression.hidden.speed_friend", Achievement: "hidden-speed-friend", Label: "Instant Connection", Caption: "Accepted a friend request within 60 seconds.", Icon: "diversity-2", Color: "#7c3aed", LocalizationKey: "badge.hidden_speed_friend", Category: "hidden", Series: nil, Hidden: true},
	{Identifier: "progression.hidden.social_butterfly_day", Achievement: "hidden-social-butterfly-day", Label: "Social Butterfly", Caption: "Made 5 friends in a single day.", Icon: "volunteer-activism", Color: "#c026d3", LocalizationKey: "badge.hidden_social_butterfly_day", Category: "hidden", Series: nil, Hidden: true},
}
