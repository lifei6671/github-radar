package metrics

type TrendingSignal struct {
	Frequency, Streak int
	Rank              *int
	Score             *float64
}

type RankedRepository struct {
	RepoID int64
	Rank   int
}

type TrendingBatch struct {
	ObservedAt int64
	Members    []RankedRepository
}

// TrendingSignals 按最新批次到最旧批次计算上榜信号，空榜也会中断连续上榜。
func TrendingSignals(asOf int64, batches []TrendingBatch) (map[int64]TrendingSignal, bool) {
	result := make(map[int64]TrendingSignal)
	if len(batches) == 0 {
		return result, false
	}
	fresh := batches[0].ObservedAt > asOf-Day
	for index, batch := range batches {
		for _, row := range batch.Members {
			s := result[row.RepoID]
			s.Frequency++
			if s.Streak == index {
				s.Streak++
			}
			if index == 0 {
				rank := row.Rank
				s.Rank = &rank
				if fresh {
					value := 100 / float64(max(rank, 1))
					s.Score = &value
				}
			}
			result[row.RepoID] = s
		}
	}
	return result, fresh
}
