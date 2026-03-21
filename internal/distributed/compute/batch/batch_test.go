package batch

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestJobCreation(t *testing.T) {
	job := NewJob(
		types.HexToAddress("0x01"),
		types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb"), Size: 1024},
		SplitByCount,
		4,
	)

	if job.Status != JobPending {
		t.Fatalf("status = %v, want Pending", job.Status)
	}
	if job.Parallelism != 4 {
		t.Fatalf("parallelism = %d, want 4", job.Parallelism)
	}
}

func TestMapReduceJobCreation(t *testing.T) {
	job := NewMapReduceJob(
		types.HexToAddress("0x01"),
		types.HexToHash("0xaa"),
		types.HexToHash("0xbb"),
		DataRef{Hash: types.HexToHash("0xcc"), Size: 2048},
		SplitBySize,
		8,
	)

	if job.ReduceHash == (types.Hash{}) {
		t.Fatal("reduce hash should be set")
	}
}

func TestJobProgress(t *testing.T) {
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	job.AddMapTask(DataRef{Hash: types.HexToHash("0xd1")}, 0)
	job.AddMapTask(DataRef{Hash: types.HexToHash("0xd2")}, 1)

	completed, total := job.Progress()
	if completed != 0 || total != 2 {
		t.Fatalf("progress = %d/%d, want 0/2", completed, total)
	}

	job.MapTasks[0].Status = JobCompleted
	completed, total = job.Progress()
	if completed != 1 || total != 2 {
		t.Fatalf("progress = %d/%d, want 1/2", completed, total)
	}
}

func TestSchedulerSubmitAndComplete(t *testing.T) {
	s := NewScheduler(100, 4)

	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{
		{Hash: types.HexToHash("0xd1"), Size: 512},
		{Hash: types.HexToHash("0xd2"), Size: 512},
	}

	err := s.SubmitJob(job, partitions)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	if job.Status != JobMapping {
		t.Fatalf("status = %v, want Mapping", job.Status)
	}
	if len(job.MapTasks) != 2 {
		t.Fatalf("map tasks = %d, want 2", len(job.MapTasks))
	}

	// Complete first task
	s.CompleteMapTask(job.ID, job.MapTasks[0].ID, DataRef{Hash: types.HexToHash("0xe1")})
	status, _ := s.CheckProgress(job.ID)
	if status != JobMapping {
		t.Fatalf("status = %v, want still Mapping", status)
	}

	// Complete second task
	s.CompleteMapTask(job.ID, job.MapTasks[1].ID, DataRef{Hash: types.HexToHash("0xe2")})
	status, _ = s.CheckProgress(job.ID)
	if status != JobCompleted {
		t.Fatalf("status = %v, want Completed (no reduce)", status)
	}
}

func TestSchedulerMapReduceFlow(t *testing.T) {
	s := NewScheduler(100, 4)

	job := NewMapReduceJob(types.HexToAddress("0x01"),
		types.HexToHash("0xaa"), types.HexToHash("0xbb"),
		DataRef{Hash: types.HexToHash("0xcc")}, SplitByCount, 2)

	partitions := []DataRef{
		{Hash: types.HexToHash("0xd1")},
	}

	s.SubmitJob(job, partitions)
	s.CompleteMapTask(job.ID, job.MapTasks[0].ID, DataRef{Hash: types.HexToHash("0xe1")})

	status, _ := s.CheckProgress(job.ID)
	if status != JobReducing {
		t.Fatalf("status = %v, want Reducing (has reduce hash)", status)
	}
}

func TestSchedulerFailedMap(t *testing.T) {
	s := NewScheduler(100, 4)

	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{
		{Hash: types.HexToHash("0xd1")},
		{Hash: types.HexToHash("0xd2")},
	}

	s.SubmitJob(job, partitions)
	s.FailMapTask(job.ID, job.MapTasks[0].ID, "computation error")

	status, _ := s.CheckProgress(job.ID)
	if status != JobFailed {
		t.Fatalf("status = %v, want Failed", status)
	}
}

func TestSchedulerNextBatch(t *testing.T) {
	s := NewScheduler(100, 2)

	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{
		{Hash: types.HexToHash("0xd1")},
		{Hash: types.HexToHash("0xd2")},
		{Hash: types.HexToHash("0xd3")},
	}

	s.SubmitJob(job, partitions)

	batch := s.NextBatch(job.ID)
	if len(batch) != 2 { // limited by parallelism
		t.Fatalf("batch size = %d, want 2", len(batch))
	}
}

func TestAggregator(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x1111")

	agg.RegisterReduceFunc(jobID, func(inputs [][]byte) ([]byte, error) {
		total := 0
		for _, in := range inputs {
			for _, b := range in {
				total += int(b)
			}
		}
		return []byte{byte(total)}, nil
	})

	agg.AddPartialResult(jobID, 0, []byte{10})
	agg.AddPartialResult(jobID, 1, []byte{20})
	agg.AddPartialResult(jobID, 2, []byte{30})

	if agg.PartialCount(jobID) != 3 {
		t.Fatalf("partial count = %d, want 3", agg.PartialCount(jobID))
	}

	result, err := agg.Reduce(jobID, 3)
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	if len(result) != 1 || result[0] != 60 {
		t.Fatalf("result = %v, want [60]", result)
	}

	// Cleanup
	agg.Cleanup(jobID)
	if agg.PartialCount(jobID) != 0 {
		t.Fatal("partials should be empty after cleanup")
	}
}

func TestAggregatorIncomplete(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x2222")

	agg.RegisterReduceFunc(jobID, func(inputs [][]byte) ([]byte, error) {
		return nil, nil
	})

	agg.AddPartialResult(jobID, 0, []byte{1})

	_, err := agg.Reduce(jobID, 3)
	if err == nil {
		t.Fatal("expected error for incomplete results")
	}
}

func TestSchedulerTooManyPartitions(t *testing.T) {
	s := NewScheduler(2, 4) // max 2 tasks

	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{
		{Hash: types.HexToHash("0xd1")},
		{Hash: types.HexToHash("0xd2")},
		{Hash: types.HexToHash("0xd3")},
	}

	err := s.SubmitJob(job, partitions)
	if err == nil {
		t.Fatal("expected error for too many partitions")
	}
}

func TestAggregatorPanicRecovery(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x3333")

	// Register a reduce function that will panic
	agg.RegisterReduceFunc(jobID, func(inputs [][]byte) ([]byte, error) {
		panic("intentional panic in reduce")
	})

	agg.AddPartialResult(jobID, 0, []byte{42})

	_, err := agg.Reduce(jobID, 1)
	if err == nil {
		t.Fatal("expected error when reduce function panics")
	}
	// The error message should mention the panic
	if err.Error() == "" {
		t.Fatal("error message should not be empty")
	}
}

func TestAggregatorNilReduceFunc(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x4444")

	err := agg.RegisterReduceFunc(jobID, nil)
	if err == nil {
		t.Fatal("expected error when registering nil reduce function")
	}
}

func TestJobStatusString(t *testing.T) {
	tests := []struct {
		status JobStatus
		want   string
	}{
		{JobPending, "pending"},
		{JobMapping, "mapping"},
		{JobReducing, "reducing"},
		{JobCompleted, "completed"},
		{JobFailed, "failed"},
		{JobStatus(255), "unknown"},
	}
	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("JobStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestJobDeadline(t *testing.T) {
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	// No deadline set — should not be expired
	if job.IsExpired() {
		t.Fatal("job with no deadline should not be expired")
	}

	// Set deadline in the past
	job.SetDeadline(time.Now().Add(-1 * time.Hour))
	if !job.IsExpired() {
		t.Fatal("job with past deadline should be expired")
	}

	// Set deadline in the future
	job.SetDeadline(time.Now().Add(1 * time.Hour))
	if job.IsExpired() {
		t.Fatal("job with future deadline should not be expired")
	}
}

func TestSchedulerCancelJob(t *testing.T) {
	s := NewScheduler(100, 4)
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{
		{Hash: types.HexToHash("0xd1")},
		{Hash: types.HexToHash("0xd2")},
	}
	if err := s.SubmitJob(job, partitions); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	// Cancel
	if err := s.CancelJob(job.ID, "user requested"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	if job.Status != JobFailed {
		t.Fatalf("job status = %v, want Failed", job.Status)
	}
	if job.Error != "user requested" {
		t.Fatalf("job error = %q, want %q", job.Error, "user requested")
	}
	for i, task := range job.MapTasks {
		if task.Status != JobFailed {
			t.Fatalf("task[%d] status = %v, want Failed", i, task.Status)
		}
		if task.Error != "cancelled: user requested" {
			t.Fatalf("task[%d] error = %q, want %q", i, task.Error, "cancelled: user requested")
		}
	}
}

func TestSchedulerCancelNonexistent(t *testing.T) {
	s := NewScheduler(100, 4)
	err := s.CancelJob(types.HexToHash("0xdead"), "no reason")
	if err == nil {
		t.Fatal("expected error when cancelling nonexistent job")
	}
}

func TestSchedulerExpireJobs(t *testing.T) {
	s := NewScheduler(100, 4)

	// Job 1: deadline in the past (should expire)
	job1 := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xb1")}, SplitByCount, 2)
	job1.SetDeadline(time.Now().Add(-1 * time.Hour))
	if err := s.SubmitJob(job1, []DataRef{{Hash: types.HexToHash("0xd1")}}); err != nil {
		t.Fatalf("SubmitJob 1: %v", err)
	}

	// Job 2: deadline in the future (should NOT expire)
	job2 := NewJob(types.HexToAddress("0x02"), types.HexToHash("0xbb"),
		DataRef{Hash: types.HexToHash("0xb2")}, SplitByCount, 2)
	job2.SetDeadline(time.Now().Add(1 * time.Hour))
	if err := s.SubmitJob(job2, []DataRef{{Hash: types.HexToHash("0xd2")}}); err != nil {
		t.Fatalf("SubmitJob 2: %v", err)
	}

	expired := s.ExpireJobs()
	if expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}

	if job1.Status != JobFailed {
		t.Fatalf("job1 status = %v, want Failed", job1.Status)
	}
	if job1.Error != "deadline exceeded" {
		t.Fatalf("job1 error = %q, want %q", job1.Error, "deadline exceeded")
	}
	if job2.Status != JobMapping {
		t.Fatalf("job2 status = %v, want Mapping (not expired)", job2.Status)
	}
}

func TestSchedulerDuplicateJob(t *testing.T) {
	s := NewScheduler(100, 4)
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{{Hash: types.HexToHash("0xd1")}}
	if err := s.SubmitJob(job, partitions); err != nil {
		t.Fatalf("first SubmitJob: %v", err)
	}

	// Submit the same job again
	err := s.SubmitJob(job, partitions)
	if err == nil {
		t.Fatal("expected error when submitting duplicate job")
	}
}

func TestSchedulerEmptyPartitions(t *testing.T) {
	s := NewScheduler(100, 4)
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	err := s.SubmitJob(job, []DataRef{})
	if err == nil {
		t.Fatal("expected error when submitting with empty partitions")
	}
}

func TestSchedulerGetJobNotFound(t *testing.T) {
	s := NewScheduler(100, 4)
	_, ok := s.GetJob(types.HexToHash("0xdead"))
	if ok {
		t.Fatal("expected not found for unknown job ID")
	}
}

func TestSchedulerListJobs(t *testing.T) {
	s := NewScheduler(100, 4)

	for i := 0; i < 3; i++ {
		job := NewJob(
			types.HexToAddress("0x01"),
			types.HexToHash("0xaa"),
			DataRef{Hash: types.BytesToHash([]byte{byte(i + 1)})},
			SplitByCount, 2,
		)
		partitions := []DataRef{{Hash: types.BytesToHash([]byte{byte(i + 10)})}}
		if err := s.SubmitJob(job, partitions); err != nil {
			t.Fatalf("SubmitJob %d: %v", i, err)
		}
	}

	jobs := s.ListJobs()
	if len(jobs) != 3 {
		t.Fatalf("ListJobs returned %d jobs, want 3", len(jobs))
	}
}

func TestSchedulerJobCount(t *testing.T) {
	s := NewScheduler(100, 4)

	for i := 0; i < 2; i++ {
		job := NewJob(
			types.HexToAddress("0x01"),
			types.HexToHash("0xaa"),
			DataRef{Hash: types.BytesToHash([]byte{byte(i + 1)})},
			SplitByCount, 2,
		)
		partitions := []DataRef{{Hash: types.BytesToHash([]byte{byte(i + 10)})}}
		if err := s.SubmitJob(job, partitions); err != nil {
			t.Fatalf("SubmitJob %d: %v", i, err)
		}
	}

	if s.JobCount() != 2 {
		t.Fatalf("JobCount = %d, want 2", s.JobCount())
	}
}

func TestJobMapOutputs(t *testing.T) {
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	job.AddMapTask(DataRef{Hash: types.HexToHash("0xd1")}, 0)
	job.AddMapTask(DataRef{Hash: types.HexToHash("0xd2")}, 1)
	job.AddMapTask(DataRef{Hash: types.HexToHash("0xd3")}, 2)

	// Complete only first and third
	job.MapTasks[0].Status = JobCompleted
	job.MapTasks[0].Output = DataRef{Hash: types.HexToHash("0xe1")}
	job.MapTasks[2].Status = JobCompleted
	job.MapTasks[2].Output = DataRef{Hash: types.HexToHash("0xe3")}

	outputs := job.MapOutputs()
	if len(outputs) != 2 {
		t.Fatalf("MapOutputs returned %d, want 2", len(outputs))
	}

	// Verify the hashes
	if outputs[0].Hash != types.HexToHash("0xe1") {
		t.Fatalf("outputs[0].Hash = %s, want 0xe1", outputs[0].Hash.Hex())
	}
	if outputs[1].Hash != types.HexToHash("0xe3") {
		t.Fatalf("outputs[1].Hash = %s, want 0xe3", outputs[1].Hash.Hex())
	}
}

func TestJobAllMapsCompleteEmpty(t *testing.T) {
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	// No map tasks added
	if job.AllMapsComplete() {
		t.Fatal("AllMapsComplete should return false for job with no map tasks")
	}
}

func TestAggregatorGetPartials(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x5555")

	agg.AddPartialResult(jobID, 0, []byte{10, 20})
	agg.AddPartialResult(jobID, 1, []byte{30, 40})

	partials := agg.GetPartials(jobID)
	if len(partials) != 2 {
		t.Fatalf("GetPartials returned %d entries, want 2", len(partials))
	}

	// Verify values
	if partials[0][0] != 10 || partials[0][1] != 20 {
		t.Fatalf("partials[0] = %v, want [10 20]", partials[0])
	}
	if partials[1][0] != 30 || partials[1][1] != 40 {
		t.Fatalf("partials[1] = %v, want [30 40]", partials[1])
	}

	// Modify returned data and verify stored data is not affected (isolation)
	partials[0][0] = 99
	partialsAgain := agg.GetPartials(jobID)
	if partialsAgain[0][0] != 10 {
		t.Fatalf("modifying returned partials affected stored data: got %d, want 10", partialsAgain[0][0])
	}
}

func TestAggregatorMissingPartition(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x6666")

	agg.RegisterReduceFunc(jobID, func(inputs [][]byte) ([]byte, error) {
		return nil, nil
	})

	// Add partitions 0 and 2, skip 1
	agg.AddPartialResult(jobID, 0, []byte{10})
	agg.AddPartialResult(jobID, 2, []byte{30})

	_, err := agg.Reduce(jobID, 3)
	if err == nil {
		t.Fatal("expected error for missing partition 1")
	}
}

func TestAggregatorNoReduceFunc(t *testing.T) {
	agg := NewAggregator()
	jobID := types.HexToHash("0x7777")

	agg.AddPartialResult(jobID, 0, []byte{10})

	_, err := agg.Reduce(jobID, 1)
	if err == nil {
		t.Fatal("expected error when no reduce function is registered")
	}
}

func TestSchedulerCompleteMapTaskNotFound(t *testing.T) {
	s := NewScheduler(100, 4)
	job := NewJob(types.HexToAddress("0x01"), types.HexToHash("0xaa"),
		DataRef{Hash: types.HexToHash("0xbb")}, SplitByCount, 2)

	partitions := []DataRef{{Hash: types.HexToHash("0xd1")}}
	if err := s.SubmitJob(job, partitions); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	// Try to complete a non-existent task
	err := s.CompleteMapTask(job.ID, types.HexToHash("0xdeadbeef"), DataRef{Hash: types.HexToHash("0xf1")})
	if err == nil {
		t.Fatal("expected error when completing non-existent task")
	}
}
