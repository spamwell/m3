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

package block

import (
	"fmt"
	"time"

	"github.com/m3db/m3/src/query/cost"
	"github.com/m3db/m3/src/query/models"
	xcost "github.com/m3db/m3/src/x/cost"

	"github.com/uber-go/tally"
)

// ColumnBlockBuilder builds a block optimized for column iteration
type ColumnBlockBuilder struct {
	block           *columnBlock
	enforcer        cost.ChainedEnforcer
	blockDatapoints tally.Counter
}

type columnBlock struct {
	blockType  BlockType
	columns    []column
	meta       Metadata
	seriesMeta []SeriesMeta
}

func (c *columnBlock) Unconsolidated() (UnconsolidatedBlock, error) {
	return nil, fmt.Errorf("unconsolidated view not supported for block, meta: %s", c.meta)
}

func (c *columnBlock) Meta() Metadata {
	return c.meta
}

func (c *columnBlock) StepIter() (StepIter, error) {
	if len(c.columns) != c.meta.Bounds.Steps() {
		return nil, fmt.Errorf("mismatch in block columns and meta bounds, columns: %d, bounds: %v", len(c.columns), c.meta.Bounds)
	}

	return &colBlockIter{
		columns:    c.columns,
		seriesMeta: c.seriesMeta,
		meta:       c.meta,
		idx:        -1,
	}, nil
}

// TODO: allow series iteration
func (c *columnBlock) SeriesIter() (SeriesIter, error) {
	return newColumnBlockSeriesIter(c.columns, c.meta, c.seriesMeta), nil
}

func (c *columnBlock) WithMetadata(
	meta Metadata,
	seriesMetas []SeriesMeta,
) (Block, error) {
	return &columnBlock{
		columns:    c.columns,
		meta:       meta,
		seriesMeta: seriesMetas,
		blockType:  BlockDecompressed,
	}, nil
}

// TODO: allow series iteration
func (c *columnBlock) SeriesMeta() []SeriesMeta {
	return c.seriesMeta
}

func (c *columnBlock) StepCount() int {
	return len(c.columns)
}

func (c *columnBlock) Info() BlockInfo {
	return NewBlockInfo(c.blockType)
}

// Close frees up any resources
// TODO: actually free up the resources
func (c *columnBlock) Close() error {
	return nil
}

type colBlockIter struct {
	idx         int
	timeForStep time.Time
	err         error
	meta        Metadata
	seriesMeta  []SeriesMeta
	columns     []column
}

func (c *colBlockIter) SeriesMeta() []SeriesMeta {
	return c.seriesMeta
}

func (c *colBlockIter) StepCount() int {
	return len(c.columns)
}

func (c *colBlockIter) Next() bool {
	if c.err != nil {
		return false
	}

	c.idx++
	next := c.idx < len(c.columns)
	if !next {
		return false
	}

	c.timeForStep, c.err = c.meta.Bounds.TimeForIndex(c.idx)
	if c.err != nil {
		return false
	}

	return next
}

func (c *colBlockIter) Err() error {
	return c.err
}

func (c *colBlockIter) Current() Step {
	col := c.columns[c.idx]
	return ColStep{
		time:   c.timeForStep,
		values: col.Values,
	}
}

func (c *colBlockIter) Close() { /*no-op*/ }

// ColStep is a single column containing data from multiple series at a given time step
type ColStep struct {
	time   time.Time
	values []float64
}

// Time for the step
func (c ColStep) Time() time.Time {
	return c.time
}

// Values for the column
func (c ColStep) Values() []float64 {
	return c.values
}

// NewColStep creates a new column step
func NewColStep(t time.Time, values []float64) Step {
	return ColStep{time: t, values: values}
}

// NewColumnBlockBuilder creates a new column block builder
func NewColumnBlockBuilder(
	queryCtx *models.QueryContext,
	meta Metadata,
	seriesMeta []SeriesMeta) Builder {
	return ColumnBlockBuilder{
		enforcer:        queryCtx.Enforcer.Child(cost.BlockLevel),
		blockDatapoints: queryCtx.Scope.Tagged(map[string]string{"type": "generated"}).Counter("datapoints"),
		block: &columnBlock{
			meta:       meta,
			seriesMeta: seriesMeta,
			blockType:  BlockDecompressed,
		},
	}
}

// AppendValue adds a value to a column at index
func (cb ColumnBlockBuilder) AppendValue(idx int, value float64) error {
	columns := cb.block.columns
	if len(columns) <= idx {
		return fmt.Errorf("idx out of range for append: %d", idx)
	}

	r := cb.enforcer.Add(1)
	if r.Error != nil {
		return r.Error
	}

	cb.blockDatapoints.Inc(1)

	columns[idx].Values = append(columns[idx].Values, value)
	return nil
}

// AppendValues adds a slice of values to a column at index
func (cb ColumnBlockBuilder) AppendValues(idx int, values []float64) error {
	columns := cb.block.columns
	if len(columns) <= idx {
		return fmt.Errorf("idx out of range for append: %d", idx)
	}

	r := cb.enforcer.Add(xcost.Cost(len(values)))
	if r.Error != nil {
		return r.Error
	}

	cb.blockDatapoints.Inc(int64(len(values)))

	columns[idx].Values = append(columns[idx].Values, values...)
	return nil
}

func (cb ColumnBlockBuilder) AddCols(num int) error {
	if num < 1 {
		return fmt.Errorf("must add more than 0 columns, adding: %d", num)
	}

	newCols := make([]column, num)
	cb.block.columns = append(cb.block.columns, newCols...)
	return nil
}

func (cb ColumnBlockBuilder) Build() Block {
	return NewAccountedBlock(cb.block, cb.enforcer)
}

func (cb ColumnBlockBuilder) BuildAsType(blockType BlockType) Block {
	cb.block.blockType = blockType
	return NewAccountedBlock(cb.block, cb.enforcer)
}

type column struct {
	Values []float64
}

// columnBlockSeriesIter is used to iterate over a column. Assumes that all columns have the same length
type columnBlockSeriesIter struct {
	idx        int
	blockMeta  Metadata
	values     []float64
	columns    []column
	seriesMeta []SeriesMeta
}

func newColumnBlockSeriesIter(
	columns []column,
	blockMeta Metadata,
	seriesMeta []SeriesMeta,
) SeriesIter {
	return &columnBlockSeriesIter{
		columns:    columns,
		blockMeta:  blockMeta,
		seriesMeta: seriesMeta,
		idx:        -1,
		values:     make([]float64, len(columns)),
	}
}

func (m *columnBlockSeriesIter) SeriesMeta() []SeriesMeta {
	return m.seriesMeta
}

func (m *columnBlockSeriesIter) SeriesCount() int {
	cols := m.columns
	if len(cols) == 0 {
		return 0
	}

	return len(cols[0].Values)
}

func (m *columnBlockSeriesIter) Err() error {
	// no-op
	return nil
}

func (m *columnBlockSeriesIter) Next() bool {
	m.idx++
	next := m.idx < m.SeriesCount()
	if !next {
		return false
	}

	cols := m.columns
	for i, col := range cols {
		m.values[i] = col.Values[m.idx]
	}

	return next
}

func (m *columnBlockSeriesIter) Current() Series {
	// TODO: pool these
	vals := make([]float64, len(m.values))
	copy(vals, m.values)
	return NewSeries(vals, m.seriesMeta[m.idx])
}

// TODO: Actually free resources once we do pooling
func (m *columnBlockSeriesIter) Close() {
}
