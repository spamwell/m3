//
// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package fanout

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/m3db/m3/src/dbnode/encoding"
	"github.com/m3db/m3/src/query/block"
	errs "github.com/m3db/m3/src/query/errors"
	"github.com/m3db/m3/src/query/models"
	"github.com/m3db/m3/src/query/policy/filter"
	"github.com/m3db/m3/src/query/storage"
	"github.com/m3db/m3/src/query/test/m3"
	"github.com/m3db/m3/src/query/test/seriesiter"
	"github.com/m3db/m3/src/query/ts"
	"github.com/m3db/m3/src/x/ident"
	"github.com/m3db/m3/src/x/instrument"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func filterFunc(output bool) filter.Storage {
	return func(query storage.Query, store storage.Storage) bool {
		return output
	}
}

func filterCompleteTagsFunc(output bool) filter.StorageCompleteTags {
	return func(query storage.CompleteTagsQuery, store storage.Storage) bool {
		return output
	}
}

func fakeIterator(t *testing.T) encoding.SeriesIterators {
	id := ident.StringID("id")
	namespace := ident.StringID("metrics")
	return encoding.NewSeriesIterators([]encoding.SeriesIterator{
		encoding.NewSeriesIterator(encoding.SeriesIteratorOptions{
			ID:        id,
			Namespace: namespace,
			Tags:      seriesiter.GenerateSingleSampleTagIterator(gomock.NewController(t), seriesiter.GenerateTag()),
		}, nil)}, nil)
}

type fetchResponse struct {
	result encoding.SeriesIterators
	err    error
}

func setupFanoutRead(t *testing.T, output bool, response ...*fetchResponse) storage.Storage {
	if len(response) == 0 {
		response = []*fetchResponse{{err: fmt.Errorf("unable to get response")}}
	}

	ctrl := gomock.NewController(t)
	store1, session1 := m3.NewStorageAndSession(t, ctrl)
	store2, session2 := m3.NewStorageAndSession(t, ctrl)

	session1.EXPECT().FetchTagged(gomock.Any(), gomock.Any(), gomock.Any()).Return(response[0].result, true, response[0].err)
	session2.EXPECT().FetchTagged(gomock.Any(), gomock.Any(), gomock.Any()).Return(response[len(response)-1].result, true, response[len(response)-1].err)
	session1.EXPECT().FetchTaggedIDs(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, false, errs.ErrNotImplemented)
	session2.EXPECT().FetchTaggedIDs(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, false, errs.ErrNotImplemented)
	session1.EXPECT().IteratorPools().
		Return(nil, nil).AnyTimes()
	session2.EXPECT().IteratorPools().
		Return(nil, nil).AnyTimes()

	stores := []storage.Storage{
		store1, store2,
	}

	store := NewStorage(stores, filterFunc(output), filterFunc(output),
		filterCompleteTagsFunc(output), instrument.NewOptions())
	return store
}

func setupFanoutWrite(t *testing.T, output bool, errs ...error) storage.Storage {
	ctrl := gomock.NewController(t)
	store1, session1 := m3.NewStorageAndSession(t, ctrl)
	store2, session2 := m3.NewStorageAndSession(t, ctrl)
	session1.EXPECT().
		WriteTagged(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errs[0])
	session1.EXPECT().IteratorPools().
		Return(nil, nil).AnyTimes()
	session1.EXPECT().FetchTaggedIDs(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, true, errs[0]).AnyTimes()
	session1.EXPECT().Aggregate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, true, errs[0]).AnyTimes()

	session2.EXPECT().
		WriteTagged(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errs[len(errs)-1])
	session2.EXPECT().IteratorPools().
		Return(nil, nil).AnyTimes()

	stores := []storage.Storage{
		store1, store2,
	}
	store := NewStorage(stores, filterFunc(output), filterFunc(output),
		filterCompleteTagsFunc(output), instrument.NewOptions())
	return store
}

func TestFanoutReadEmpty(t *testing.T) {
	store := setupFanoutRead(t, false)
	res, err := store.Fetch(context.TODO(), nil, nil)
	assert.NoError(t, err, "No error")
	require.NotNil(t, res, "Non empty result")
	assert.Len(t, res.SeriesList, 0, "No series")
}

func TestFanoutReadError(t *testing.T) {
	store := setupFanoutRead(t, true)
	opts := storage.NewFetchOptions()
	_, err := store.Fetch(context.TODO(), &storage.FetchQuery{}, opts)
	assert.Error(t, err)
}

func TestFanoutReadSuccess(t *testing.T) {
	store := setupFanoutRead(t, true, &fetchResponse{
		result: fakeIterator(t)},
		&fetchResponse{result: fakeIterator(t)},
	)
	res, err := store.Fetch(context.TODO(), &storage.FetchQuery{
		Start: time.Now().Add(-time.Hour),
		End:   time.Now(),
	}, storage.NewFetchOptions())
	require.NoError(t, err, "no error on read")
	assert.NotNil(t, res)
	assert.NoError(t, store.Close())
}

func TestFanoutSearchEmpty(t *testing.T) {
	store := setupFanoutRead(t, false)
	res, err := store.SearchSeries(context.TODO(), nil, nil)
	assert.NoError(t, err, "No error")
	require.NotNil(t, res, "Non empty result")
	assert.Len(t, res.Metrics, 0, "No series")
}

func TestFanoutSearchError(t *testing.T) {
	store := setupFanoutRead(t, true)
	opts := storage.NewFetchOptions()
	_, err := store.SearchSeries(context.TODO(), &storage.FetchQuery{}, opts)
	assert.Error(t, err)
}

func TestFanoutWriteEmpty(t *testing.T) {
	store := setupFanoutWrite(t, false, fmt.Errorf("write error"))
	err := store.Write(context.TODO(), nil)
	assert.NoError(t, err)
}

func TestFanoutWriteError(t *testing.T) {
	store := setupFanoutWrite(t, true, fmt.Errorf("write error"))
	datapoints := make(ts.Datapoints, 1)
	datapoints[0] = ts.Datapoint{Timestamp: time.Now(), Value: 1}
	err := store.Write(context.TODO(), &storage.WriteQuery{
		Datapoints: datapoints,
		Tags:       models.NewTags(0, nil),
	})
	assert.Error(t, err)
}

func TestFanoutWriteSuccess(t *testing.T) {
	store := setupFanoutWrite(t, true, nil)
	datapoints := make(ts.Datapoints, 1)
	datapoints[0] = ts.Datapoint{Timestamp: time.Now(), Value: 1}
	err := store.Write(context.TODO(), &storage.WriteQuery{
		Datapoints: datapoints,
		Tags:       models.NewTags(0, nil),
		Attributes: storage.Attributes{
			MetricsType: storage.UnaggregatedMetricsType,
		},
	})
	assert.NoError(t, err)
}

func TestCompleteTagsError(t *testing.T) {
	store := setupFanoutWrite(t, true, fmt.Errorf("err"))
	datapoints := make(ts.Datapoints, 1)
	datapoints[0] = ts.Datapoint{Timestamp: time.Now(), Value: 1}
	_, err := store.CompleteTags(
		context.TODO(),
		&storage.CompleteTagsQuery{
			CompleteNameOnly: true,
			TagMatchers:      models.Matchers{},
		},
		storage.NewFetchOptions(),
	)
	assert.Error(t, err)
}

// Error continuation tests below.
func TestFanoutSearchErrorContinues(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	filter := func(_ storage.Query, _ storage.Storage) bool { return true }
	tFilter := func(_ storage.CompleteTagsQuery, _ storage.Storage) bool { return true }
	okStore := storage.NewMockStorage(ctrl)
	okStore.EXPECT().SearchSeries(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			&storage.SearchResults{
				Metrics: models.Metrics{
					models.Metric{
						ID: []byte("ok"),
					},
				},
			},
			nil,
		)

	warnStore := storage.NewMockStorage(ctrl)
	warnStore.EXPECT().SearchSeries(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			&storage.SearchResults{
				Metrics: models.Metrics{
					models.Metric{
						ID: []byte("warn"),
					},
				},
			},
			errors.New("e"),
		)
	warnStore.EXPECT().ErrorBehavior().Return(storage.BehaviorWarn)
	warnStore.EXPECT().Name().Return("warn")

	stores := []storage.Storage{warnStore, okStore}
	store := NewStorage(stores, filter, filter, tFilter, instrument.NewOptions())
	opts := storage.NewFetchOptions()
	result, err := store.SearchSeries(context.TODO(), &storage.FetchQuery{}, opts)
	assert.NoError(t, err)

	require.Equal(t, 1, len(result.Metrics))
	assert.Equal(t, []byte("ok"), result.Metrics[0].ID)
}

func TestFanoutCompleteTagsErrorContinues(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	filter := func(_ storage.Query, _ storage.Storage) bool { return true }
	tFilter := func(_ storage.CompleteTagsQuery, _ storage.Storage) bool { return true }
	okStore := storage.NewMockStorage(ctrl)
	okStore.EXPECT().CompleteTags(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			&storage.CompleteTagsResult{
				CompleteNameOnly: true,
				CompletedTags: []storage.CompletedTag{
					storage.CompletedTag{
						Name: []byte("ok"),
					},
				},
			},
			nil,
		)

	warnStore := storage.NewMockStorage(ctrl)
	warnStore.EXPECT().CompleteTags(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			&storage.CompleteTagsResult{
				CompleteNameOnly: true,
				CompletedTags: []storage.CompletedTag{
					storage.CompletedTag{
						Name: []byte("warn"),
					},
				},
			},
			errors.New("e"),
		)
	warnStore.EXPECT().ErrorBehavior().Return(storage.BehaviorWarn)
	warnStore.EXPECT().Name().Return("warn")

	stores := []storage.Storage{warnStore, okStore}
	store := NewStorage(stores, filter, filter, tFilter, instrument.NewOptions())
	opts := storage.NewFetchOptions()
	q := &storage.CompleteTagsQuery{CompleteNameOnly: true}
	result, err := store.CompleteTags(context.TODO(), q, opts)
	assert.NoError(t, err)

	require.True(t, result.CompleteNameOnly)
	require.Equal(t, 1, len(result.CompletedTags))
	assert.Equal(t, []byte("ok"), result.CompletedTags[0].Name)
}

func TestFanoutFetchBlocksErrorContinues(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	filter := func(_ storage.Query, _ storage.Storage) bool { return true }
	tFilter := func(_ storage.CompleteTagsQuery, _ storage.Storage) bool { return true }
	okBlock := block.NewScalar(1, block.Metadata{})
	okStore := storage.NewMockStorage(ctrl)
	okStore.EXPECT().FetchBlocks(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			block.Result{
				Blocks: []block.Block{okBlock},
			},
			nil,
		)

	warnStore := storage.NewMockStorage(ctrl)
	warnBlock := block.NewScalar(2, block.Metadata{})
	warnStore.EXPECT().FetchBlocks(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			block.Result{
				Blocks: []block.Block{warnBlock},
			},
			errors.New("e"),
		)
	warnStore.EXPECT().ErrorBehavior().Return(storage.BehaviorWarn)
	warnStore.EXPECT().Name().Return("warn")

	stores := []storage.Storage{warnStore, okStore}
	store := NewStorage(stores, filter, filter, tFilter, instrument.NewOptions())
	opts := storage.NewFetchOptions()
	result, err := store.FetchBlocks(context.TODO(), &storage.FetchQuery{}, opts)
	assert.NoError(t, err)

	require.Equal(t, 1, len(result.Blocks))
	scalar, ok := (result.Blocks[0]).(*block.Scalar)
	require.True(t, ok)
	assert.Equal(t, 1.0, scalar.Value())
}

func TestFanoutFetchErrorContinues(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	filter := func(_ storage.Query, _ storage.Storage) bool { return true }
	tFilter := func(_ storage.CompleteTagsQuery, _ storage.Storage) bool { return true }
	okStore := storage.NewMockStorage(ctrl)
	okStore.EXPECT().Fetch(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			&storage.FetchResult{
				SeriesList: ts.SeriesList{
					ts.NewSeries([]byte("ok"), nil, models.Tags{}),
				},
			},
			nil,
		)
	okStore.EXPECT().Type().Return(storage.TypeLocalDC)

	warnStore := storage.NewMockStorage(ctrl)
	warnStore.EXPECT().Fetch(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(
			&storage.FetchResult{
				SeriesList: ts.SeriesList{
					ts.NewSeries([]byte("warn"), nil, models.Tags{}),
				},
			},
			errors.New("e"),
		)
	warnStore.EXPECT().ErrorBehavior().Return(storage.BehaviorWarn)
	warnStore.EXPECT().Name().Return("warn")

	stores := []storage.Storage{warnStore, okStore}
	store := NewStorage(stores, filter, filter, tFilter, instrument.NewOptions())
	opts := storage.NewFetchOptions()
	result, err := store.Fetch(context.TODO(), &storage.FetchQuery{}, opts)
	assert.NoError(t, err)

	require.Equal(t, 1, len(result.SeriesList))
	assert.Equal(t, []byte("ok"), result.SeriesList[0].Name())
}
