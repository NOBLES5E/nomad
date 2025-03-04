package allochealth

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/nomad/ci"
	"github.com/hashicorp/nomad/client/serviceregistration"
	"github.com/hashicorp/nomad/client/serviceregistration/checks/checkstore"
	regmock "github.com/hashicorp/nomad/client/serviceregistration/mock"
	"github.com/hashicorp/nomad/client/state"
	cstructs "github.com/hashicorp/nomad/client/structs"
	"github.com/hashicorp/nomad/helper"
	"github.com/hashicorp/nomad/helper/testlog"
	"github.com/hashicorp/nomad/nomad/mock"
	"github.com/hashicorp/nomad/nomad/structs"
	"github.com/hashicorp/nomad/testutil"
	"github.com/shoenig/test/must"
	"github.com/stretchr/testify/require"
)

func TestTracker_ConsulChecks_Healthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.Alloc()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
	task := alloc.Job.TaskGroups[0].Tasks[0]

	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		task.Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
	}

	// Make Consul response
	check := &consulapi.AgentCheck{
		Name:   task.Services[0].Checks[0].Name,
		Status: consulapi.HealthPassing,
	}
	taskRegs := map[string]*serviceregistration.ServiceRegistrations{
		task.Name: {
			Services: map[string]*serviceregistration.ServiceRegistration{
				task.Services[0].Name: {
					Service: &consulapi.AgentService{
						ID:      "foo",
						Service: task.Services[0].Name,
					},
					Checks: []*consulapi.AgentCheck{check},
				},
			},
		},
	}

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	// Don't reply on the first call
	var called uint64
	consul := regmock.NewServiceRegistrationHandler(logger)
	consul.AllocRegistrationsFn = func(string) (*serviceregistration.AllocRegistration, error) {
		if atomic.AddUint64(&called, 1) == 1 {
			return nil, nil
		}

		reg := &serviceregistration.AllocRegistration{
			Tasks: taskRegs,
		}

		return reg, nil
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	select {
	case <-time.After(4 * checkInterval):
		require.Fail(t, "timed out while waiting for health")
	case h := <-tracker.HealthyCh():
		require.True(t, h)
	}
}

func TestTracker_NomadChecks_Healthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.Alloc()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
	alloc.Job.TaskGroups[0].Tasks[0].Services[0].Provider = "nomad"

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		alloc.Job.TaskGroups[0].Tasks[0].Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
	}

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	err := checks.Set(alloc.ID, &structs.CheckQueryResult{
		ID:        "abc123",
		Mode:      "healthiness",
		Status:    "pending",
		Output:    "nomad: waiting to run",
		Timestamp: time.Now().Unix(),
		Group:     alloc.TaskGroup,
		Task:      alloc.Job.TaskGroups[0].Tasks[0].Name,
		Service:   alloc.Job.TaskGroups[0].Tasks[0].Services[0].Name,
		Check:     alloc.Job.TaskGroups[0].Tasks[0].Services[0].Checks[0].Name,
	})
	must.NoError(t, err)

	consul := regmock.NewServiceRegistrationHandler(logger)
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	go func() {
		// wait a bit then update the check to passing
		time.Sleep(15 * time.Millisecond)
		must.NoError(t, checks.Set(alloc.ID, &structs.CheckQueryResult{
			ID:        "abc123",
			Mode:      "healthiness",
			Status:    "success",
			Output:    "nomad: http ok",
			Timestamp: time.Now().Unix(),
			Group:     alloc.TaskGroup,
			Task:      alloc.Job.TaskGroups[0].Tasks[0].Name,
			Service:   alloc.Job.TaskGroups[0].Tasks[0].Services[0].Name,
			Check:     alloc.Job.TaskGroups[0].Tasks[0].Services[0].Checks[0].Name,
		}))
	}()

	select {
	case <-time.After(4 * checkInterval):
		t.Fatalf("timed out while waiting for success")
	case healthy := <-tracker.HealthyCh():
		must.True(t, healthy)
	}
}

func TestTracker_NomadChecks_Unhealthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.Alloc()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
	alloc.Job.TaskGroups[0].Tasks[0].Services[0].Provider = "nomad"

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		alloc.Job.TaskGroups[0].Tasks[0].Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
	}

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	err := checks.Set(alloc.ID, &structs.CheckQueryResult{
		ID:        "abc123",
		Mode:      "healthiness",
		Status:    "pending", // start out pending
		Output:    "nomad: waiting to run",
		Timestamp: time.Now().Unix(),
		Group:     alloc.TaskGroup,
		Task:      alloc.Job.TaskGroups[0].Tasks[0].Name,
		Service:   alloc.Job.TaskGroups[0].Tasks[0].Services[0].Name,
		Check:     alloc.Job.TaskGroups[0].Tasks[0].Services[0].Checks[0].Name,
	})
	must.NoError(t, err)

	consul := regmock.NewServiceRegistrationHandler(logger)
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	go func() {
		// wait a bit then update the check to failing
		time.Sleep(15 * time.Millisecond)
		must.NoError(t, checks.Set(alloc.ID, &structs.CheckQueryResult{
			ID:        "abc123",
			Mode:      "healthiness",
			Status:    "failing",
			Output:    "connection refused",
			Timestamp: time.Now().Unix(),
			Group:     alloc.TaskGroup,
			Task:      alloc.Job.TaskGroups[0].Tasks[0].Name,
			Service:   alloc.Job.TaskGroups[0].Tasks[0].Services[0].Name,
			Check:     alloc.Job.TaskGroups[0].Tasks[0].Services[0].Checks[0].Name,
		}))
	}()

	// make sure we are always unhealthy across 4 check intervals
	for i := 0; i < 4; i++ {
		<-time.After(checkInterval)
		select {
		case <-tracker.HealthyCh():
			t.Fatalf("should not receive on healthy chan with failing check")
		default:
		}
	}
}

func TestTracker_Checks_PendingPostStop_Healthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.LifecycleAllocWithPoststopDeploy()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up

	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		"web": {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
		"post": {
			State: structs.TaskStatePending,
		},
	}

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	consul := regmock.NewServiceRegistrationHandler(logger)
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	select {
	case <-time.After(4 * checkInterval):
		require.Fail(t, "timed out while waiting for health")
	case h := <-tracker.HealthyCh():
		require.True(t, h)
	}
}

func TestTracker_Succeeded_PostStart_Healthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.LifecycleAllocWithPoststartDeploy()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = time.Millisecond * 1
	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		"web": {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
		"post": {
			State:      structs.TaskStateDead,
			StartedAt:  time.Now(),
			FinishedAt: time.Now().Add(alloc.Job.TaskGroups[0].Migrate.MinHealthyTime / 2),
		},
	}

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	consul := regmock.NewServiceRegistrationHandler(logger)
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, alloc.Job.TaskGroups[0].Migrate.MinHealthyTime, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	select {
	case <-time.After(alloc.Job.TaskGroups[0].Migrate.MinHealthyTime * 2):
		require.Fail(t, "timed out while waiting for health")
	case h := <-tracker.HealthyCh():
		require.True(t, h)
	}
}

func TestTracker_ConsulChecks_Unhealthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.Alloc()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
	task := alloc.Job.TaskGroups[0].Tasks[0]

	newCheck := task.Services[0].Checks[0].Copy()
	newCheck.Name = "failing-check"
	task.Services[0].Checks = append(task.Services[0].Checks, newCheck)

	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		task.Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
	}

	// Make Consul response
	checkHealthy := &consulapi.AgentCheck{
		Name:   task.Services[0].Checks[0].Name,
		Status: consulapi.HealthPassing,
	}
	checksUnhealthy := &consulapi.AgentCheck{
		Name:   task.Services[0].Checks[1].Name,
		Status: consulapi.HealthCritical,
	}
	taskRegs := map[string]*serviceregistration.ServiceRegistrations{
		task.Name: {
			Services: map[string]*serviceregistration.ServiceRegistration{
				task.Services[0].Name: {
					Service: &consulapi.AgentService{
						ID:      "foo",
						Service: task.Services[0].Name,
					},
					Checks: []*consulapi.AgentCheck{checkHealthy, checksUnhealthy},
				},
			},
		},
	}

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	// Don't reply on the first call
	var called uint64
	consul := regmock.NewServiceRegistrationHandler(logger)
	consul.AllocRegistrationsFn = func(string) (*serviceregistration.AllocRegistration, error) {
		if atomic.AddUint64(&called, 1) == 1 {
			return nil, nil
		}

		reg := &serviceregistration.AllocRegistration{
			Tasks: taskRegs,
		}

		return reg, nil
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	testutil.WaitForResult(func() (bool, error) {
		lookup := atomic.LoadUint64(&called)
		return lookup < 4, fmt.Errorf("wait to get more task registration lookups: %v", lookup)
	}, func(err error) {
		require.NoError(t, err)
	})

	tracker.lock.Lock()
	require.False(t, tracker.checksHealthy)
	tracker.lock.Unlock()

	select {
	case v := <-tracker.HealthyCh():
		require.Failf(t, "expected no health value", " got %v", v)
	default:
		// good
	}
}

func TestTracker_Healthy_IfBothTasksAndConsulChecksAreHealthy(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.Alloc()
	logger := testlog.HCLogger(t)

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	tracker := NewTracker(ctx, logger, alloc, nil, nil, nil, time.Millisecond, true)

	assertNoHealth := func() {
		require.NoError(t, tracker.ctx.Err())
		select {
		case v := <-tracker.HealthyCh():
			require.Failf(t, "unexpected healthy event", "got %v", v)
		default:
		}
	}

	// first set task health without checks
	tracker.setTaskHealth(true, false)
	assertNoHealth()

	// now fail task health again before checks are successful
	tracker.setTaskHealth(false, false)
	assertNoHealth()

	// now pass health checks - do not propagate health yet
	tracker.setCheckHealth(true)
	assertNoHealth()

	// set tasks to healthy - don't propagate health yet, wait for the next check
	tracker.setTaskHealth(true, false)
	assertNoHealth()

	// set checks to true, now propagate health status
	tracker.setCheckHealth(true)

	require.Error(t, tracker.ctx.Err())
	select {
	case v := <-tracker.HealthyCh():
		require.True(t, v)
	default:
		require.Fail(t, "expected a health status")
	}
}

// TestTracker_Checks_Healthy_Before_TaskHealth asserts that we mark an alloc
// healthy, if the checks pass before task health pass
func TestTracker_Checks_Healthy_Before_TaskHealth(t *testing.T) {
	ci.Parallel(t)

	alloc := mock.Alloc()
	alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
	task := alloc.Job.TaskGroups[0].Tasks[0]

	// new task starting unhealthy, without services
	task2 := task.Copy()
	task2.Name = task2.Name + "2"
	task2.Services = nil
	alloc.Job.TaskGroups[0].Tasks = append(alloc.Job.TaskGroups[0].Tasks, task2)

	// Synthesize running alloc and tasks
	alloc.ClientStatus = structs.AllocClientStatusRunning
	alloc.TaskStates = map[string]*structs.TaskState{
		task.Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
		task2.Name: {
			State: structs.TaskStatePending,
		},
	}

	// Make Consul response
	check := &consulapi.AgentCheck{
		Name:   task.Services[0].Checks[0].Name,
		Status: consulapi.HealthPassing,
	}
	taskRegs := map[string]*serviceregistration.ServiceRegistrations{
		task.Name: {
			Services: map[string]*serviceregistration.ServiceRegistration{
				task.Services[0].Name: {
					Service: &consulapi.AgentService{
						ID:      "foo",
						Service: task.Services[0].Name,
					},
					Checks: []*consulapi.AgentCheck{check},
				},
			},
		},
	}

	logger := testlog.HCLogger(t)
	b := cstructs.NewAllocBroadcaster(logger)
	defer b.Close()

	// Don't reply on the first call
	var called uint64
	consul := regmock.NewServiceRegistrationHandler(logger)
	consul.AllocRegistrationsFn = func(string) (*serviceregistration.AllocRegistration, error) {
		if atomic.AddUint64(&called, 1) == 1 {
			return nil, nil
		}

		reg := &serviceregistration.AllocRegistration{
			Tasks: taskRegs,
		}

		return reg, nil
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	checks := checkstore.NewStore(logger, state.NewMemDB(logger))
	checkInterval := 10 * time.Millisecond
	tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
	tracker.checkLookupInterval = checkInterval
	tracker.Start()

	// assert that we don't get marked healthy
	select {
	case <-time.After(4 * checkInterval):
		// still unhealthy, good
	case h := <-tracker.HealthyCh():
		require.Fail(t, "unexpected health event", h)
	}

	helper.WithLock(&tracker.lock, func() {
		require.False(t, tracker.tasksHealthy)
		require.False(t, tracker.checksHealthy)
	})

	// now set task to healthy
	runningAlloc := alloc.Copy()
	runningAlloc.TaskStates = map[string]*structs.TaskState{
		task.Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
		task2.Name: {
			State:     structs.TaskStateRunning,
			StartedAt: time.Now(),
		},
	}
	err := b.Send(runningAlloc)
	require.NoError(t, err)

	// eventually, it is marked as healthy
	select {
	case <-time.After(4 * checkInterval):
		require.Fail(t, "timed out while waiting for health")
	case h := <-tracker.HealthyCh():
		require.True(t, h)
	}

}

func TestTracker_ConsulChecks_OnUpdate(t *testing.T) {
	ci.Parallel(t)

	cases := []struct {
		desc          string
		checkOnUpdate string
		consulResp    string
		expectedPass  bool
	}{
		{
			desc:          "check require_healthy consul healthy",
			checkOnUpdate: structs.OnUpdateRequireHealthy,
			consulResp:    consulapi.HealthPassing,
			expectedPass:  true,
		},
		{
			desc:          "check on_update ignore_warning, consul warn",
			checkOnUpdate: structs.OnUpdateIgnoreWarn,
			consulResp:    consulapi.HealthWarning,
			expectedPass:  true,
		},
		{
			desc:          "check on_update ignore_warning, consul critical",
			checkOnUpdate: structs.OnUpdateIgnoreWarn,
			consulResp:    consulapi.HealthCritical,
			expectedPass:  false,
		},
		{
			desc:          "check on_update ignore_warning, consul healthy",
			checkOnUpdate: structs.OnUpdateIgnoreWarn,
			consulResp:    consulapi.HealthPassing,
			expectedPass:  true,
		},
		{
			desc:          "check on_update ignore, consul critical",
			checkOnUpdate: structs.OnUpdateIgnore,
			consulResp:    consulapi.HealthCritical,
			expectedPass:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {

			alloc := mock.Alloc()
			alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
			task := alloc.Job.TaskGroups[0].Tasks[0]

			// Synthesize running alloc and tasks
			alloc.ClientStatus = structs.AllocClientStatusRunning
			alloc.TaskStates = map[string]*structs.TaskState{
				task.Name: {
					State:     structs.TaskStateRunning,
					StartedAt: time.Now(),
				},
			}

			// Make Consul response
			check := &consulapi.AgentCheck{
				Name:   task.Services[0].Checks[0].Name,
				Status: tc.consulResp,
			}
			taskRegs := map[string]*serviceregistration.ServiceRegistrations{
				task.Name: {
					Services: map[string]*serviceregistration.ServiceRegistration{
						task.Services[0].Name: {
							Service: &consulapi.AgentService{
								ID:      "foo",
								Service: task.Services[0].Name,
							},
							Checks: []*consulapi.AgentCheck{check},
							CheckOnUpdate: map[string]string{
								check.CheckID: tc.checkOnUpdate,
							},
						},
					},
				},
			}

			logger := testlog.HCLogger(t)
			b := cstructs.NewAllocBroadcaster(logger)
			defer b.Close()

			// Don't reply on the first call
			var called uint64
			consul := regmock.NewServiceRegistrationHandler(logger)
			consul.AllocRegistrationsFn = func(string) (*serviceregistration.AllocRegistration, error) {
				if atomic.AddUint64(&called, 1) == 1 {
					return nil, nil
				}

				reg := &serviceregistration.AllocRegistration{
					Tasks: taskRegs,
				}

				return reg, nil
			}

			ctx, cancelFn := context.WithCancel(context.Background())
			defer cancelFn()

			checks := checkstore.NewStore(logger, state.NewMemDB(logger))
			checkInterval := 10 * time.Millisecond
			tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, time.Millisecond, true)
			tracker.checkLookupInterval = checkInterval
			tracker.Start()

			select {
			case <-time.After(4 * checkInterval):
				if !tc.expectedPass {
					// tracker should still be running
					require.Nil(t, tracker.ctx.Err())
					return
				}
				require.Fail(t, "timed out while waiting for health")
			case h := <-tracker.HealthyCh():
				require.True(t, h)
			}

			// For healthy checks, the tracker should stop watching
			select {
			case <-tracker.ctx.Done():
				// Ok, tracker should exit after reporting healthy
			default:
				require.Fail(t, "expected tracker to exit after reporting healthy")
			}
		})
	}
}

func TestTracker_NomadChecks_OnUpdate(t *testing.T) {
	ci.Parallel(t)

	cases := []struct {
		name         string
		checkMode    structs.CheckMode
		checkResult  structs.CheckStatus
		expectedPass bool
	}{
		{
			name:         "mode is healthiness and check is healthy",
			checkMode:    structs.Healthiness,
			checkResult:  structs.CheckSuccess,
			expectedPass: true,
		},
		{
			name:         "mode is healthiness and check is unhealthy",
			checkMode:    structs.Healthiness,
			checkResult:  structs.CheckFailure,
			expectedPass: false,
		},
		{
			name:         "mode is readiness and check is healthy",
			checkMode:    structs.Readiness,
			checkResult:  structs.CheckSuccess,
			expectedPass: true,
		},
		{
			name:         "mode is readiness and check is healthy",
			checkMode:    structs.Readiness,
			checkResult:  structs.CheckFailure,
			expectedPass: true,
		},
	}

	for i := range cases {
		tc := cases[i]
		t.Run(tc.name, func(t *testing.T) {
			alloc := mock.Alloc()
			alloc.Job.TaskGroups[0].Migrate.MinHealthyTime = 1 // let's speed things up
			alloc.Job.TaskGroups[0].Tasks[0].Services[0].Provider = "nomad"

			logger := testlog.HCLogger(t)
			b := cstructs.NewAllocBroadcaster(logger)
			defer b.Close()

			// Synthesize running alloc and tasks
			alloc.ClientStatus = structs.AllocClientStatusRunning
			alloc.TaskStates = map[string]*structs.TaskState{
				alloc.Job.TaskGroups[0].Tasks[0].Name: {
					State:     structs.TaskStateRunning,
					StartedAt: time.Now(),
				},
			}

			// Set a check that is pending
			checks := checkstore.NewStore(logger, state.NewMemDB(logger))
			err := checks.Set(alloc.ID, &structs.CheckQueryResult{
				ID:        "abc123",
				Mode:      tc.checkMode,
				Status:    structs.CheckPending,
				Output:    "nomad: waiting to run",
				Timestamp: time.Now().Unix(),
				Group:     alloc.TaskGroup,
				Task:      alloc.Job.TaskGroups[0].Tasks[0].Name,
				Service:   alloc.Job.TaskGroups[0].Tasks[0].Services[0].Name,
				Check:     alloc.Job.TaskGroups[0].Tasks[0].Services[0].Checks[0].Name,
			})
			must.NoError(t, err)

			go func() {
				// wait a bit then update the check to passing
				time.Sleep(15 * time.Millisecond)
				must.NoError(t, checks.Set(alloc.ID, &structs.CheckQueryResult{
					ID:        "abc123",
					Mode:      tc.checkMode,
					Status:    tc.checkResult,
					Output:    "some output",
					Timestamp: time.Now().Unix(),
					Group:     alloc.TaskGroup,
					Task:      alloc.Job.TaskGroups[0].Tasks[0].Name,
					Service:   alloc.Job.TaskGroups[0].Tasks[0].Services[0].Name,
					Check:     alloc.Job.TaskGroups[0].Tasks[0].Services[0].Checks[0].Name,
				}))
			}()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			consul := regmock.NewServiceRegistrationHandler(logger)
			minHealthyTime := 1 * time.Millisecond
			tracker := NewTracker(ctx, logger, alloc, b.Listen(), consul, checks, minHealthyTime, true)
			tracker.checkLookupInterval = 10 * time.Millisecond
			tracker.Start()

			select {
			case <-time.After(8 * tracker.checkLookupInterval):
				if !tc.expectedPass {
					// tracker should still be running
					must.NoError(t, tracker.ctx.Err())
					return
				}
				t.Fatal("timed out while waiting for health")
			case h := <-tracker.HealthyCh():
				require.True(t, h)
			}

			// For healthy checks, the tracker should stop watching
			select {
			case <-tracker.ctx.Done():
				// Ok, tracker should exit after reporting healthy
			default:
				t.Fatal("expected tracker to exit after reporting healthy")
			}
		})
	}
}
