package batch

import (
	"testing"

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
