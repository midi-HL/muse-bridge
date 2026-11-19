package core

import (
	"context"
	"errors"
	"strconv"
	"time"
)

type TelegramDailyUsage struct {
	Used, Limit int // Limit == 0 means unlimited.
	Timezone    string
	ResetsAt    time.Time
}

// TelegramUsage reads the daily bucket Chat uses for an accepted sender.
// An anonymous sender (0) uses the shared bucket for that chat.
func (s *Service) TelegramUsage(ctx context.Context, chatID, senderID int64) (TelegramDailyUsage, error) {
	return s.telegramUsageAt(ctx, chatID, senderID, time.Now())
}

func (s *Service) telegramUsageAt(ctx context.Context, chatID, senderID int64, now time.Time) (TelegramDailyUsage, error) {
	if chatID == 0 || senderID < 0 {
		return TelegramDailyUsage{}, errors.New("invalid Telegram usage identity")
	}
	subject, err := chatDailySubject(ChatRequest{
		Platform: "telegram", UserID: strconv.FormatInt(chatID, 10), RetryUserID: strconv.FormatInt(senderID, 10),
	})
	if err != nil {
		return TelegramDailyUsage{}, err
	}
	settings := s.Settings()
	zone, loc, err := chatDailyLocation(settings)
	if err != nil {
		return TelegramDailyUsage{}, err
	}
	local := now.In(loc)
	used, err := s.db.ReadCounter(ctx, "chat_day", subject, chatDailyBucket(zone, local))
	if err != nil {
		return TelegramDailyUsage{}, err
	}
	return TelegramDailyUsage{
		Used: used, Limit: settingInt(settings, "limits.daily_per_user", 100),
		Timezone: zone, ResetsAt: nextChatDay(local),
	}, nil
}

func nextChatDay(local time.Time) time.Time {
	y, m, d := local.Date()
	start := local.Truncate(time.Second)
	previous := start
	// Search across the next three days for the first instant whose local
	// calendar date differs. This handles offset jumps that make midnight
	// nonexistent or whose offset changes before the following midnight.
	for minute := 1; minute <= 72*60; minute++ {
		candidate := start.Add(time.Duration(minute) * time.Minute)
		cy, cm, cd := candidate.In(local.Location()).Date()
		if cy != y || cm != m || cd != d {
			low, high := previous.Unix(), candidate.Unix()
			for high-low > 1 {
				mid := low + (high-low)/2
				probe := time.Unix(mid, 0).In(local.Location())
				py, pm, pd := probe.Date()
				if py == y && pm == m && pd == d {
					low = mid
				} else {
					high = mid
				}
			}
			return time.Unix(high, 0).In(local.Location())
		}
		previous = candidate
	}
	return time.Date(y, m, d+1, 0, 0, 0, 0, local.Location())
}
