package collection

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pachyderm/pachyderm/src/client"
	"github.com/pachyderm/pachyderm/src/client/pfs"
	"github.com/pachyderm/pachyderm/src/client/pkg/require"
	"github.com/pachyderm/pachyderm/src/client/pps"
	"github.com/pachyderm/pachyderm/src/server/pkg/uuid"
	"github.com/pachyderm/pachyderm/src/server/pkg/watch"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/gogo/protobuf/types"
)

var (
	pipelineIndex Index = Index{
		Field: "Pipeline",
		Multi: false,
	}
	commitMultiIndex Index = Index{
		Field: "Provenance",
		Multi: true,
	}
)

func TestIndex(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()

	jobInfos := NewCollection(etcdClient, uuidPrefix, []Index{pipelineIndex}, &pps.JobInfo{}, nil)

	j1 := &pps.JobInfo{
		Job:      &pps.Job{"j1"},
		Pipeline: &pps.Pipeline{"p1"},
	}
	j2 := &pps.JobInfo{
		Job:      &pps.Job{"j2"},
		Pipeline: &pps.Pipeline{"p1"},
	}
	j3 := &pps.JobInfo{
		Job:      &pps.Job{"j3"},
		Pipeline: &pps.Pipeline{"p2"},
	}
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		jobInfos := jobInfos.ReadWrite(stm)
		jobInfos.Put(j1.Job.ID, j1)
		jobInfos.Put(j2.Job.ID, j2)
		jobInfos.Put(j3.Job.ID, j3)
		return nil
	})
	require.NoError(t, err)

	jobInfosReadonly := jobInfos.ReadOnly(context.Background())

	iter, err := jobInfosReadonly.GetByIndex(pipelineIndex, j1.Pipeline)
	require.NoError(t, err)
	var ID string
	job := new(pps.JobInfo)
	ok, err := iter.Next(&ID, job)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, j1.Job.ID, ID)
	require.Equal(t, j1, job)
	ok, err = iter.Next(&ID, job)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, j2.Job.ID, ID)
	require.Equal(t, j2, job)
	ok, err = iter.Next(&ID, job)
	require.NoError(t, err)
	require.False(t, ok)

	iter, err = jobInfosReadonly.GetByIndex(pipelineIndex, j3.Pipeline)
	require.NoError(t, err)
	ok, err = iter.Next(&ID, job)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, j3.Job.ID, ID)
	require.Equal(t, j3, job)
	ok, err = iter.Next(&ID, job)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestIndexWatch(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()

	jobInfos := NewCollection(etcdClient, uuidPrefix, []Index{pipelineIndex}, &pps.JobInfo{}, nil)

	j1 := &pps.JobInfo{
		Job:      &pps.Job{"j1"},
		Pipeline: &pps.Pipeline{"p1"},
	}
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		jobInfos := jobInfos.ReadWrite(stm)
		jobInfos.Put(j1.Job.ID, j1)
		return nil
	})
	require.NoError(t, err)

	jobInfosReadonly := jobInfos.ReadOnly(context.Background())

	watcher, err := jobInfosReadonly.WatchByIndex(pipelineIndex, j1.Pipeline)
	eventCh := watcher.Watch()
	require.NoError(t, err)
	var ID string
	job := new(pps.JobInfo)
	event := <-eventCh
	require.NoError(t, event.Err)
	require.Equal(t, event.Type, watch.EventPut)
	require.NoError(t, event.Unmarshal(&ID, job))
	require.Equal(t, j1.Job.ID, ID)
	require.Equal(t, j1, job)

	// Now we will put j1 again, unchanged.  We want to make sure
	// that we do not receive an event.
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		jobInfos := jobInfos.ReadWrite(stm)
		jobInfos.Put(j1.Job.ID, j1)
		return nil
	})

	select {
	case event := <-eventCh:
		t.Fatalf("should not have received an event %v", event)
	case <-time.After(2 * time.Second):
	}

	j2 := &pps.JobInfo{
		Job:      &pps.Job{"j2"},
		Pipeline: &pps.Pipeline{"p1"},
	}

	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		jobInfos := jobInfos.ReadWrite(stm)
		jobInfos.Put(j2.Job.ID, j2)
		return nil
	})
	require.NoError(t, err)

	event = <-eventCh
	require.NoError(t, event.Err)
	require.Equal(t, event.Type, watch.EventPut)
	require.NoError(t, event.Unmarshal(&ID, job))
	require.Equal(t, j2.Job.ID, ID)
	require.Equal(t, j2, job)

	j1Prime := &pps.JobInfo{
		Job:      &pps.Job{"j1"},
		Pipeline: &pps.Pipeline{"p3"},
	}
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		jobInfos := jobInfos.ReadWrite(stm)
		jobInfos.Put(j1.Job.ID, j1Prime)
		return nil
	})
	require.NoError(t, err)

	event = <-eventCh
	require.NoError(t, event.Err)
	require.Equal(t, event.Type, watch.EventDelete)
	require.NoError(t, event.Unmarshal(&ID, job))
	require.Equal(t, j1.Job.ID, ID)

	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		jobInfos := jobInfos.ReadWrite(stm)
		jobInfos.Delete(j2.Job.ID)
		return nil
	})
	require.NoError(t, err)

	event = <-eventCh
	require.NoError(t, event.Err)
	require.Equal(t, event.Type, watch.EventDelete)
	require.NoError(t, event.Unmarshal(&ID, job))
	require.Equal(t, j2.Job.ID, ID)
}

func TestMultiIndex(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()

	cis := NewCollection(etcdClient, uuidPrefix, []Index{commitMultiIndex}, &pfs.CommitInfo{}, nil)

	c1 := &pfs.CommitInfo{
		Commit: client.NewCommit("repo", "c1"),
		Provenance: []*pfs.Commit{
			client.NewCommit("in", "c1"),
			client.NewCommit("in", "c2"),
			client.NewCommit("in", "c3"),
		},
	}
	c2 := &pfs.CommitInfo{
		Commit: client.NewCommit("repo", "c2"),
		Provenance: []*pfs.Commit{
			client.NewCommit("in", "c1"),
			client.NewCommit("in", "c2"),
			client.NewCommit("in", "c3"),
		},
	}
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		cis := cis.ReadWrite(stm)
		cis.Put(c1.Commit.ID, c1)
		cis.Put(c2.Commit.ID, c2)
		return nil
	})
	require.NoError(t, err)

	cisReadonly := cis.ReadOnly(context.Background())

	// Test that the first key retrieves both r1 and r2
	iter, err := cisReadonly.GetByIndex(commitMultiIndex, client.NewCommit("in", "c1"))
	require.NoError(t, err)
	var ID string
	ci := &pfs.CommitInfo{}
	ok, err := iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c1.Commit.ID, ID)
	require.Equal(t, c1, ci)
	ci = &pfs.CommitInfo{}
	ok, err = iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c2.Commit.ID, ID)
	require.Equal(t, c2, ci)

	// Test that the second key retrieves both r1 and r2
	iter, err = cisReadonly.GetByIndex(commitMultiIndex, client.NewCommit("in", "c2"))
	require.NoError(t, err)
	ci = &pfs.CommitInfo{}
	ok, err = iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c1.Commit.ID, ID)
	require.Equal(t, c1, ci)
	ci = &pfs.CommitInfo{}
	ok, err = iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c2.Commit.ID, ID)
	require.Equal(t, c2, ci)

	// replace "c3" in the provenance of c1 with "c4"
	c1.Provenance[2].ID = "c4"
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		cis := cis.ReadWrite(stm)
		cis.Put(c1.Commit.ID, c1)
		return nil
	})
	require.NoError(t, err)

	// Now "c3" only retrieves c2 (indexes are updated)
	iter, err = cisReadonly.GetByIndex(commitMultiIndex, client.NewCommit("in", "c3"))
	require.NoError(t, err)
	ci = &pfs.CommitInfo{}
	ok, err = iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c2.Commit.ID, ID)
	require.Equal(t, c2, ci)

	// And "C4" only retrieves r1 (indexes are updated)
	iter, err = cisReadonly.GetByIndex(commitMultiIndex, client.NewCommit("in", "c4"))
	require.NoError(t, err)
	ci = &pfs.CommitInfo{}
	ok, err = iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c1.Commit.ID, ID)
	require.Equal(t, c1, ci)

	// Delete c1 from etcd completely
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		cis := cis.ReadWrite(stm)
		cis.Delete(c1.Commit.ID)
		return nil
	})

	// Now "c1" only retrieves c2
	iter, err = cisReadonly.GetByIndex(commitMultiIndex, client.NewCommit("in", "c1"))
	require.NoError(t, err)
	ci = &pfs.CommitInfo{}
	ok, err = iter.Next(&ID, ci)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, c2.Commit.ID, ID)
	require.Equal(t, c2, ci)
}

func TestBoolIndex(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()
	boolValues := NewCollection(etcdClient, uuidPrefix, []Index{{
		Field: "Value",
		Multi: false,
	}}, &types.BoolValue{}, nil)

	r1 := &types.BoolValue{
		Value: true,
	}
	r2 := &types.BoolValue{
		Value: false,
	}
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		boolValues := boolValues.ReadWrite(stm)
		boolValues.Put("true", r1)
		boolValues.Put("false", r2)
		return nil
	})
	require.NoError(t, err)

	// Test that we don't format the index string incorrectly
	resp, err := etcdClient.Get(context.Background(), uuidPrefix, etcd.WithPrefix())
	require.NoError(t, err)
	for _, kv := range resp.Kvs {
		if !bytes.Contains(kv.Key, []byte("__index_")) {
			continue // not an index record
		}
		require.True(t,
			bytes.Contains(kv.Key, []byte("__index_Value/true")) ||
				bytes.Contains(kv.Key, []byte("__index_Value/false")), string(kv.Key))
	}
}

var epsilon = &types.BoolValue{Value: true}

func TestTTL(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()

	clxn := NewCollection(etcdClient, uuidPrefix, nil, &types.BoolValue{}, nil)
	const TTL = 5
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		return clxn.ReadWrite(stm).PutTTL("key", epsilon, TTL)
	})
	require.NoError(t, err)

	var actualTTL int64
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		var err error
		actualTTL, err = clxn.ReadWrite(stm).TTL("key")
		return err
	})
	require.NoError(t, err)
	require.True(t, actualTTL > 0 && actualTTL < TTL, "actualTTL was %v", actualTTL)
}

func TestTTLExpire(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()

	clxn := NewCollection(etcdClient, uuidPrefix, nil, &types.BoolValue{}, nil)
	const TTL = 5
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		return clxn.ReadWrite(stm).PutTTL("key", epsilon, TTL)
	})
	require.NoError(t, err)

	time.Sleep((TTL + 1) * time.Second)
	value := &types.BoolValue{}
	err = clxn.ReadOnly(context.Background()).Get("key", value)
	require.NotNil(t, err)
	require.Matches(t, "not found", err.Error())
}

func TestTTLExtend(t *testing.T) {
	etcdClient := getEtcdClient()
	uuidPrefix := uuid.NewWithoutDashes()

	// Put value with short TLL & check that it was set
	clxn := NewCollection(etcdClient, uuidPrefix, nil, &types.BoolValue{}, nil)
	const TTL = 5
	_, err := NewSTM(context.Background(), etcdClient, func(stm STM) error {
		return clxn.ReadWrite(stm).PutTTL("key", epsilon, TTL)
	})
	require.NoError(t, err)

	var actualTTL int64
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		var err error
		actualTTL, err = clxn.ReadWrite(stm).TTL("key")
		return err
	})
	require.NoError(t, err)
	require.True(t, actualTTL > 0 && actualTTL < TTL, "actualTTL was %v", actualTTL)

	// Put value with new, longer TLL and check that it was set
	const LongerTTL = 15
	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		return clxn.ReadWrite(stm).PutTTL("key", epsilon, LongerTTL)
	})
	require.NoError(t, err)

	_, err = NewSTM(context.Background(), etcdClient, func(stm STM) error {
		var err error
		actualTTL, err = clxn.ReadWrite(stm).TTL("key")
		return err
	})
	require.NoError(t, err)
	require.True(t, actualTTL > TTL && actualTTL < LongerTTL, "actualTTL was %v", actualTTL)
}

var etcdClient *etcd.Client
var etcdClientOnce sync.Once

func getEtcdClient() *etcd.Client {
	etcdClientOnce.Do(func() {
		var err error
		etcdClient, err = etcd.New(etcd.Config{
			Endpoints:   []string{"localhost:32379"},
			DialOptions: client.EtcdDialOptions(),
		})
		if err != nil {
			panic(err)
		}
	})
	return etcdClient
}
