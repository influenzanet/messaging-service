package types

import "time"

type MessageCounter struct {
	Total     int
	Failed    int
	Success   int
	StartTime int64
	Duration  int64
}

func (mc *MessageCounter) IncreaseCounter(success bool) {
	mc.Total += 1
	if success {
		mc.Success += 1
	} else {
		mc.Failed += 1
	}
	mc.Duration = time.Now().Unix() - mc.StartTime
}

func (mc *MessageCounter) Stop() {
	mc.Duration = time.Now().Unix() - mc.StartTime
}

func InitMessageCounter() MessageCounter {
	return MessageCounter{
		Total:     0,
		Failed:    0,
		Success:   0,
		StartTime: time.Now().Unix(),
	}
}
