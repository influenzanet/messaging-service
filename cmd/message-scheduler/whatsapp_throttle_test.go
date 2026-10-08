package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"go.mongodb.org/mongo-driver/bson"
)

func throttledByMeta(retryAfter time.Duration) error {
	return &waClient.WhatsAppSendError{StatusCode: http.StatusTooManyRequests, Code: 130429, RetryAfter: retryAfter}
}

func (db *schedulerTestDB) throttleState(t *testing.T) (notBefore int64, level int) {
	t.Helper()
	state, err := db.service.GetWhatsAppThrottle(schedulerTestInstanceID)
	if err != nil {
		t.Fatalf("read the throttle state: %v", err)
	}
	return state.NotBefore, state.Level
}

// endPause moves the stored wait into the past, as if it had been served, and keeps the level.
func (db *schedulerTestDB) endPause(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.database.Collection("whatsapp-throttle").UpdateOne(ctx, bson.M{"_id": "sender"}, bson.M{"$set": bson.M{"notBefore": 1}}); err != nil {
		t.Fatalf("end the pause: %v", err)
	}
}

func (db *schedulerTestDB) countThrottleDocs(t *testing.T) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := db.database.Collection("whatsapp-throttle").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count throttle documents: %v", err)
	}
	return n
}

func (db *schedulerTestDB) lockedCount(t *testing.T) int64 {
	t.Helper()
	return db.countQueued(t, bson.M{"lastSendAttempt": bson.M{"$ne": 0}})
}

func TestWhatsAppThrottleDelay(t *testing.T) {
	const lock = 10 * time.Second
	t.Run("grows with the level", func(t *testing.T) {
		previous := time.Duration(0)
		for level := 0; level < 12; level++ {
			got := whatsAppThrottleDelay(level, 0, lock, 0)
			if level == 0 && got != whatsAppThrottleBase {
				t.Fatalf("level 0 must wait the base, got %v", got)
			}
			if got < previous {
				t.Fatalf("level %d waits %v, less than the %v of the level before", level, got, previous)
			}
			previous = got
		}
		if whatsAppThrottleDelay(1, 0, lock, 0) != 2*whatsAppThrottleBase || whatsAppThrottleDelay(2, 0, lock, 0) != 4*whatsAppThrottleBase {
			t.Fatal("each level must double the wait")
		}
	})
	t.Run("cap is respected whatever the level and the jitter", func(t *testing.T) {
		for _, level := range []int{6, 7, 20, 1000, 1 << 30} {
			for _, jitter := range []float64{0, 0.5, 0.999999} {
				if got := whatsAppThrottleDelay(level, 0, lock, jitter); got > whatsAppThrottleCap {
					t.Fatalf("level %d jitter %v waits %v, above the cap", level, jitter, got)
				}
			}
		}
		if got := whatsAppThrottleDelay(1<<30, 0, lock, 0); got != whatsAppThrottleCap {
			t.Fatalf("a long series must reach the cap, got %v", got)
		}
	})
	t.Run("jitter only lengthens the wait and stays bounded", func(t *testing.T) {
		low := whatsAppThrottleDelay(2, 0, lock, 0)
		high := whatsAppThrottleDelay(2, 0, lock, 0.999999)
		if high <= low {
			t.Fatalf("jitter must lengthen the wait: %v then %v", low, high)
		}
		if limit := time.Duration(float64(low) * (1 + whatsAppThrottleJitter)); high > limit {
			t.Fatalf("jitter %v exceeds its share of the wait, limit %v", high, limit)
		}
	})
	t.Run("Retry-After wins when it is longer, the backoff when it is shorter", func(t *testing.T) {
		if got := whatsAppThrottleDelay(0, 10*time.Minute, lock, 0); got != 10*time.Minute {
			t.Fatalf("a longer Retry-After must be honoured, got %v", got)
		}
		if got := whatsAppThrottleDelay(3, 5*time.Second, lock, 0); got != 8*whatsAppThrottleBase {
			t.Fatalf("a shorter Retry-After must not shorten the backoff, got %v", got)
		}
	})
	t.Run("never shorter than the claim lock", func(t *testing.T) {
		if got := whatsAppThrottleDelay(0, 0, 10*time.Minute, 0); got != 10*time.Minute {
			t.Fatalf("the pause must outlast the locks, got %v", got)
		}
	})
	t.Run("not before is rounded up", func(t *testing.T) {
		now := time.Unix(1000, 400*int64(time.Millisecond))
		if got := whatsAppThrottleNotBefore(now, 30*time.Second); got < 1030 || got > 1031 || time.Unix(got, 0).Before(now.Add(30*time.Second)) {
			t.Fatalf("got %d", got)
		}
	})
}

func TestWhatsAppThrottleStopsTickChargesNothingAndRecordsPause(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 3, 2)
	sender := &fakeWhatsAppSender{err: throttledByMeta(0)}

	before := time.Now().Unix()
	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 1 {
		t.Fatalf("a throttle must stop the tick at once, got %d calls", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 3 || sent != 0 {
		t.Fatalf("queue changed: outgoing=%d sent=%d", outgoing, sent)
	}
	if changed := db.countQueued(t, bson.M{"sendAttempt": bson.M{"$ne": 2}}); changed != 0 {
		t.Fatalf("a throttle charged %d messages", changed)
	}
	notBefore, level := db.throttleState(t)
	if level != 1 {
		t.Fatalf("level = %d, want 1", level)
	}
	min := before + int64(whatsAppThrottleBase/time.Second)
	max := time.Now().Unix() + int64(whatsAppThrottleBase/time.Second)*5/4 + 2
	if notBefore < min || notBefore > max {
		t.Fatalf("notBefore %d outside [%d, %d]", notBefore, min, max)
	}
}

func TestWhatsAppPausedTickMakesNoSenderCallAndClaimsNothing(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 3, 0)
	sender := &fakeWhatsAppSender{err: throttledByMeta(0)}
	runWhatsAppHandler(db.service, sender, 60)
	db.expireLocks(t)
	_, levelBefore := db.throttleState(t)

	ok := &fakeWhatsAppSender{}
	runWhatsAppHandler(db.service, ok, 60)

	if ok.callCount() != 0 {
		t.Fatalf("a paused tick called the sender %d times", ok.callCount())
	}
	if locked := db.countQueued(t, bson.M{"lastSendAttempt": bson.M{"$gt": time.Now().Add(-time.Minute).Unix()}}); locked != 0 {
		t.Fatalf("a paused tick claimed %d messages", locked)
	}
	if _, level := db.throttleState(t); level != levelBefore {
		t.Fatalf("a paused tick changed the level to %d", level)
	}
}

func TestWhatsAppSendsAgainAfterThePauseAndResetsTheLevel(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 2, 0)
	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: throttledByMeta(0)}, 60)
	db.endPause(t)
	db.expireLocks(t)

	ok := &fakeWhatsAppSender{}
	runWhatsAppHandler(db.service, ok, 60)

	if ok.callCount() != 2 {
		t.Fatalf("after the pause both messages must be sent, got %d calls", ok.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 2 {
		t.Fatalf("outgoing=%d sent=%d", outgoing, sent)
	}
	notBefore, level := db.throttleState(t)
	if level != 0 {
		t.Fatalf("an accepted message must reset the level, got %d", level)
	}
	if notBefore != 1 {
		t.Fatalf("the reset must not touch notBefore, got %d", notBefore)
	}
}

func TestWhatsAppSuccessWithoutThrottleWritesNoState(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 2, 0)
	runWhatsAppHandler(db.service, &fakeWhatsAppSender{}, 60)
	if n := db.countThrottleDocs(t); n != 0 {
		t.Fatalf("a send with no throttle created %d throttle documents", n)
	}
}

func TestWhatsAppConsecutiveThrottlesGrowTheWait(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, 0)
	sender := &fakeWhatsAppSender{err: throttledByMeta(0)}

	base := int64(whatsAppThrottleBase / time.Second)
	for series := 0; series < 4; series++ {
		runWhatsAppHandler(db.service, sender, 60)
		notBefore, level := db.throttleState(t)
		if level != series+1 {
			t.Fatalf("throttle %d: level %d", series+1, level)
		}
		wait := notBefore - time.Now().Unix()
		low := base << series
		high := low*5/4 + 2
		if wait < low-2 || wait > high {
			t.Fatalf("throttle %d waits %d s, want between %d and %d", series+1, wait, low, high)
		}
		db.endPause(t)
		db.expireLocks(t)
	}
	if msg := db.oneOutgoing(t); msg.SendAttempt != 0 {
		t.Fatalf("the series of throttles charged %d attempts", msg.SendAttempt)
	}
}

func TestWhatsAppRetryAfterLongerThanTheBackoffIsHonoured(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, 0)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: throttledByMeta(30 * time.Minute)}, 60)

	notBefore, _ := db.throttleState(t)
	wait := notBefore - time.Now().Unix()
	if wait < 30*60-2 || wait > 30*60+2 {
		t.Fatalf("waits %d s, want the 1800 s Meta asked for", wait)
	}
}

func TestWhatsAppShortRetryAfterDoesNotShortenTheBackoff(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, 0)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: throttledByMeta(2 * time.Second)}, 60)

	notBefore, _ := db.throttleState(t)
	if wait := notBefore - time.Now().Unix(); wait < int64(whatsAppThrottleBase/time.Second)-2 {
		t.Fatalf("a 2 s Retry-After shortened the wait to %d s", wait)
	}
}

func TestWhatsAppPauseOutlastsTheClaimLock(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 3, 0)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: throttledByMeta(0)}, 600)

	notBefore, _ := db.throttleState(t)
	if wait := notBefore - time.Now().Unix(); wait < 598 {
		t.Fatalf("the pause (%d s) ends before the 600 s locks of the unsent messages", wait)
	}
}

func TestWhatsAppOtherFailuresDoNotPause(t *testing.T) {
	for name, err := range map[string]error{
		"authentication": &waClient.WhatsAppSendError{StatusCode: http.StatusUnauthorized},
		"recipient":      &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131056},
		"transient":      &waClient.WhatsAppSendError{StatusCode: http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			db := newSchedulerTestDB(t)
			db.add(t, 2, 0)
			runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: err}, 60)
			if n := db.countThrottleDocs(t); n != 0 {
				t.Fatalf("a %s failure stored a pause", name)
			}
		})
	}
}

func TestWhatsAppThrottledMessageKeepsItsAttemptsAcrossTheWholeBackoff(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, maxWhatsAppSendAttempts-1)
	sender := &fakeWhatsAppSender{err: throttledByMeta(0)}
	for i := 0; i < 8; i++ {
		runWhatsAppHandler(db.service, sender, 60)
		db.endPause(t)
		db.expireLocks(t)
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
		t.Fatalf("a message on its last attempt was archived by throttling: outgoing=%d sent=%d", outgoing, sent)
	}
	if msg := db.oneOutgoing(t); msg.SendAttempt != maxWhatsAppSendAttempts-1 {
		t.Fatalf("attempts changed to %d", msg.SendAttempt)
	}
}

// Concurrency: the schedulers of an instance run in several replicas, so the pause is only
// as safe as the database update that stores it.

func recordConcurrently(t *testing.T, db *schedulerTestDB, notBefores []int64, now int64, stagger time.Duration) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, nb := range notBefores {
		wg.Add(1)
		go func(i int, nb int64) {
			defer wg.Done()
			<-start
			time.Sleep(time.Duration(i) * stagger)
			if _, err := db.service.RecordWhatsAppThrottle(schedulerTestInstanceID, nb, now); err != nil {
				t.Errorf("record throttle: %v", err)
			}
		}(i, nb)
	}
	close(start)
	wg.Wait()
}

func (db *schedulerTestDB) record(t *testing.T, notBefore, now int64) WhatsAppThrottleState {
	t.Helper()
	state, err := db.service.RecordWhatsAppThrottle(schedulerTestInstanceID, notBefore, now)
	if err != nil {
		t.Fatalf("record throttle: %v", err)
	}
	return WhatsAppThrottleState{NotBefore: state.NotBefore, Level: state.Level}
}

func (db *schedulerTestDB) reset(t *testing.T, now int64) {
	t.Helper()
	if err := db.service.ResetWhatsAppThrottleLevel(schedulerTestInstanceID, now); err != nil {
		t.Fatalf("reset throttle level: %v", err)
	}
}

// WhatsAppThrottleState mirrors the stored state for assertions.
type WhatsAppThrottleState struct {
	NotBefore int64
	Level     int
}

// Every record below uses a now earlier than every notBefore it writes, as replicas do when
// they are throttled by the same Meta event.
const throttleEventNow = int64(4_000_000_000)

func TestWhatsAppThrottleBurstOfOneEventCountsOnceAndKeepsTheLongestWait(t *testing.T) {
	db := newSchedulerTestDB(t)
	const replicas = 40
	notBefores := make([]int64, replicas)
	var longest int64
	for i := range notBefores {
		notBefores[i] = 5_000_000_000 + int64((i*37)%replicas)*10
		if notBefores[i] > longest {
			longest = notBefores[i]
		}
	}

	recordConcurrently(t, db, notBefores, throttleEventNow, 0)

	notBefore, level := db.throttleState(t)
	if notBefore != longest {
		t.Fatalf("notBefore %d, want the longest %d", notBefore, longest)
	}
	if level != 1 {
		t.Fatalf("level %d, want 1: one Meta event must count once", level)
	}
	if n := db.countThrottleDocs(t); n != 1 {
		t.Fatalf("%d throttle documents, want 1", n)
	}
}

func TestWhatsAppThrottleSeriesGrowsWhenEachPauseIsServed(t *testing.T) {
	db := newSchedulerTestDB(t)
	now := throttleEventNow
	for series := 1; series <= 4; series++ {
		notBefore := now + 60
		state := db.record(t, notBefore, now)
		if state.Level != series || state.NotBefore != notBefore {
			t.Fatalf("throttle %d: state %+v", series, state)
		}
		// The next throttle comes once the pause has been served.
		now = notBefore
	}
}

func TestWhatsAppThrottleDeterministicOverlapNeverShortensTheWaitAndCountsOnce(t *testing.T) {
	long, short := int64(5_000_003_600), int64(5_000_000_060)

	// The long wait is stored first and a shorter one, computed meanwhile by another replica,
	// arrives after it inside the same pause.
	db := newSchedulerTestDB(t)
	db.record(t, long, throttleEventNow)
	if state := db.record(t, short, throttleEventNow+1); state.NotBefore != long || state.Level != 1 {
		t.Fatalf("state after the overlap: %+v", state)
	}

	// And the other order: the short one first, the long one extends it.
	db2 := newSchedulerTestDB(t)
	db2.record(t, short, throttleEventNow)
	if state := db2.record(t, long, throttleEventNow+1); state.NotBefore != long || state.Level != 1 {
		t.Fatalf("state after the reverse order: %+v", state)
	}
}

func TestWhatsAppThrottleStaggeredRecordsKeepTheLongestWaitAndCountOnce(t *testing.T) {
	db := newSchedulerTestDB(t)
	// Waits that shrink while the replicas arrive one after the other.
	notBefores := []int64{5_000_003_600, 5_000_001_800, 5_000_000_900, 5_000_000_300, 5_000_000_060, 5_000_000_030}

	recordConcurrently(t, db, notBefores, throttleEventNow, 15*time.Millisecond)

	notBefore, level := db.throttleState(t)
	if notBefore != notBefores[0] || level != 1 {
		t.Fatalf("notBefore %d level %d, want %d and 1", notBefore, level, notBefores[0])
	}

	// Waits that grow while the replicas arrive one after the other.
	db2 := newSchedulerTestDB(t)
	reversed := []int64{notBefores[5], notBefores[4], notBefores[3], notBefores[2], notBefores[1], notBefores[0]}
	recordConcurrently(t, db2, reversed, throttleEventNow, 15*time.Millisecond)
	if state := db2.record(t, notBefores[5], throttleEventNow); state.NotBefore != notBefores[0] || state.Level != 1 {
		t.Fatalf("state after growing waits: %+v", state)
	}
}

func TestWhatsAppResetDoesNothingDuringAnActivePause(t *testing.T) {
	db := newSchedulerTestDB(t)
	notBefore := int64(5_000_000_060)
	db.record(t, notBefore, throttleEventNow)

	db.reset(t, throttleEventNow+1)
	if _, level := db.throttleState(t); level != 1 {
		t.Fatalf("a reset during an active pause changed the level to %d", level)
	}

	db.reset(t, notBefore)
	gotNotBefore, level := db.throttleState(t)
	if level != 0 {
		t.Fatalf("a reset after the pause left the level at %d", level)
	}
	if gotNotBefore != notBefore {
		t.Fatalf("the reset changed notBefore to %d", gotNotBefore)
	}
}

func TestWhatsAppResetOfAnOlderTickKeepsTheSeriesOfALaterThrottle(t *testing.T) {
	db := newSchedulerTestDB(t)
	first := int64(5_000_000_060)
	db.record(t, first, throttleEventNow)

	// Replica B is throttled once the first pause is served; replica A, whose tick started
	// earlier, then gets a success and tries to reset with its own clock.
	second := first + 120
	if state := db.record(t, second, first); state.Level != 2 {
		t.Fatalf("second throttle: %+v", state)
	}
	db.reset(t, second-1)

	if notBefore, level := db.throttleState(t); level != 2 || notBefore != second {
		t.Fatalf("A's reset wiped B's throttle: notBefore %d level %d", notBefore, level)
	}
}

func TestWhatsAppResetNeverLowersTheWaitNorCancelsAnActivePause(t *testing.T) {
	db := newSchedulerTestDB(t)
	const throttles = 20
	longest := int64(5_000_000_000 + throttles*10)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 1; i <= throttles; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, err := db.service.RecordWhatsAppThrottle(schedulerTestInstanceID, 5_000_000_000+int64(i)*10, throttleEventNow); err != nil {
				t.Errorf("record: %v", err)
			}
		}(i)
		go func() {
			defer wg.Done()
			<-start
			if err := db.service.ResetWhatsAppThrottleLevel(schedulerTestInstanceID, throttleEventNow); err != nil {
				t.Errorf("reset: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	notBefore, level := db.throttleState(t)
	if notBefore != longest {
		t.Fatalf("a reset changed notBefore: %d, want %d", notBefore, longest)
	}
	if level != 1 {
		t.Fatalf("level %d, want 1: one event, and no reset may cancel an active pause", level)
	}
	// Once the pause is served the reset applies, and the next throttle starts a new series
	// without lowering the stored wait.
	db.reset(t, longest)
	if state := db.record(t, longest-100, longest); state.Level != 1 || state.NotBefore != longest {
		t.Fatalf("state after reset and a shorter throttle: %+v", state)
	}
}

func TestWhatsAppConcurrentReplicasBackOffByTheLongestRetryAfter(t *testing.T) {
	db := newSchedulerTestDB(t)
	const replicas = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	startedAt := time.Now().Unix()
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sendErr := &waClient.WhatsAppSendError{StatusCode: http.StatusTooManyRequests, RetryAfter: time.Duration(1000+i*100) * time.Second}
			pauseWhatsAppAfterThrottle(db.service, schedulerTestInstanceID, sendErr, 60, 0)
		}(i)
	}
	close(start)
	wg.Wait()

	notBefore, level := db.throttleState(t)
	longest := int64(1000 + (replicas-1)*100)
	if notBefore < startedAt+longest || notBefore > time.Now().Unix()+longest+1 {
		t.Fatalf("notBefore %d does not match the longest Retry-After (%d s from %d)", notBefore, longest, startedAt)
	}
	if level != 1 {
		t.Fatalf("level %d, want 1: replicas throttled together are one event", level)
	}
}

func TestWhatsAppConcurrentTicksWhilePausedMakeNoSenderCall(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 30, 0)
	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: throttledByMeta(0)}, 60)
	db.expireLocks(t)

	sender := &fakeWhatsAppSender{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			<-start
			handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, 60, defaultWhatsAppTemplateRetryDelay, &wg)
		}()
	}
	close(start)
	wg.Wait()

	if sender.callCount() != 0 {
		t.Fatalf("%d sender calls while paused", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 30 || sent != 0 {
		t.Fatalf("outgoing=%d sent=%d", outgoing, sent)
	}
	if locked := db.countQueued(t, bson.M{"lastSendAttempt": bson.M{"$gt": time.Now().Add(-time.Minute).Unix()}}); locked != 0 {
		t.Fatalf("%d messages claimed while paused", locked)
	}
}

func TestWhatsAppConcurrentTicksThrottledTogetherCountOneEvent(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 30, 1)
	sender := &fakeWhatsAppSender{err: throttledByMeta(0)}

	const ticks = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < ticks; i++ {
		wg.Add(1)
		go func() {
			<-start
			handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, 60, defaultWhatsAppTemplateRetryDelay, &wg)
		}()
	}
	close(start)
	wg.Wait()

	// Ticks that reached Meta before the pause was stored were throttled by the same event,
	// whatever their number: the series grows once.
	_, level := db.throttleState(t)
	if level != 1 || sender.callCount() < 1 {
		t.Fatalf("level %d for %d sender calls, want level 1", level, sender.callCount())
	}
	if changed := db.countQueued(t, bson.M{"sendAttempt": bson.M{"$ne": 1}}); changed != 0 {
		t.Fatalf("throttling charged %d messages", changed)
	}
	if outgoing, sent := db.counts(t); outgoing != 30 || sent != 0 {
		t.Fatalf("outgoing=%d sent=%d", outgoing, sent)
	}
}

// pauseRecordingSender plays another replica: on its n-th call it stores a pause in the
// database, then answers like Meta accepting the message.
type pauseRecordingSender struct {
	t      *testing.T
	db     *schedulerTestDB
	onCall int

	mu    sync.Mutex
	calls int
}

func (s *pauseRecordingSender) SendTemplateMessage(context.Context, string, string, string, map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == s.onCall {
		now := time.Now().Unix()
		if _, err := s.db.service.RecordWhatsAppThrottle(schedulerTestInstanceID, now+600, now); err != nil {
			s.t.Errorf("record the other replica's pause: %v", err)
		}
	}
	return nil
}

func TestWhatsAppTickStopsBeforeTheNextBatchWhenAnotherReplicaPaused(t *testing.T) {
	db := newSchedulerTestDB(t)
	const extra = 5
	db.add(t, outgoingBatchSize+extra, 0)
	// The pause is stored while the last message of the first batch is being sent.
	sender := &pauseRecordingSender{t: t, db: db, onCall: outgoingBatchSize}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.calls != outgoingBatchSize {
		t.Fatalf("%d sender calls, want %d: the tick fetched and sent a batch after the pause", sender.calls, outgoingBatchSize)
	}
	if outgoing, sent := db.counts(t); outgoing != extra || sent != outgoingBatchSize {
		t.Fatalf("outgoing=%d sent=%d", outgoing, sent)
	}
	if locked := db.lockedCount(t); locked != 0 {
		t.Fatalf("the stopped tick claimed %d more messages", locked)
	}
	if changed := db.countQueued(t, bson.M{"sendAttempt": bson.M{"$ne": 0}}); changed != 0 {
		t.Fatalf("the stop charged %d messages", changed)
	}
	if _, level := db.throttleState(t); level != 1 {
		t.Fatalf("level %d, want 1", level)
	}
}
