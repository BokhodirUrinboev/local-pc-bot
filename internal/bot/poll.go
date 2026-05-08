package bot

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Polled — bitta Telegram update + uning topic ID'si.
// tgbotapi v5.5.1 message_thread_id'ni tushunmaydi, shuning uchun raw JSON'dan
// alohida chiqarib olamiz va Update bilan qo'shib uzatamiz.
type Polled struct {
	Update          tgbotapi.Update
	MessageThreadID int
}

// PollUpdates long-polling orqali updates'ni oladi va Polled channel'iga uzatadi.
// ctx tugaganda channel yopiladi.
func PollUpdates(ctx context.Context, api *tgbotapi.BotAPI, timeout int) <-chan Polled {
	out := make(chan Polled, 16)
	go func() {
		defer close(out)
		offset := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			cfg := tgbotapi.UpdateConfig{Offset: offset, Timeout: timeout}
			resp, err := api.Request(cfg)
			if err != nil {
				log.Printf("getUpdates: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			var updates []tgbotapi.Update
			if err := json.Unmarshal(resp.Result, &updates); err != nil {
				log.Printf("getUpdates unmarshal: %v", err)
				continue
			}
			var threadRaw []struct {
				Message *struct {
					MessageThreadID int `json:"message_thread_id"`
				} `json:"message"`
			}
			_ = json.Unmarshal(resp.Result, &threadRaw)
			for i, u := range updates {
				if u.UpdateID >= offset {
					offset = u.UpdateID + 1
				}
				tid := 0
				if i < len(threadRaw) && threadRaw[i].Message != nil {
					tid = threadRaw[i].Message.MessageThreadID
				}
				select {
				case <-ctx.Done():
					return
				case out <- Polled{Update: u, MessageThreadID: tid}:
				}
			}
		}
	}()
	return out
}

// SendInThread sendMessage'ni message_thread_id bilan yuboradi.
// threadID == 0 — General topic yoki oddiy chat.
func SendInThread(api *tgbotapi.BotAPI, chatID int64, threadID int, text, parseMode string, replyMarkup interface{}) (tgbotapi.Message, error) {
	params := tgbotapi.Params{
		"chat_id": strconv.FormatInt(chatID, 10),
		"text":    text,
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if threadID > 0 {
		params["message_thread_id"] = strconv.Itoa(threadID)
	}
	if replyMarkup != nil {
		if b, err := json.Marshal(replyMarkup); err == nil {
			params["reply_markup"] = string(b)
		}
	}
	resp, err := api.MakeRequest("sendMessage", params)
	if err != nil {
		return tgbotapi.Message{}, err
	}
	var m tgbotapi.Message
	if err := json.Unmarshal(resp.Result, &m); err != nil {
		return tgbotapi.Message{}, err
	}
	return m, nil
}
