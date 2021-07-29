// Copyright 2021 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package chunk

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pingcap/log"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"go.uber.org/zap"
)

var (
	equal = "="
	lt    = "<"
	lte   = "<="
	gt    = ">"
	gte   = ">="

	bucketMode = "bucketMode"
	normalMode = "normalMode"
)

type ChunkType int

const (
	Bucket ChunkType = iota + 1
	Random
	Others
)

// Bound represents a bound for a column
type Bound struct {
	Column string `json:"column"`
	Lower  string `json:"lower"`
	Upper  string `json:"upper"`

	HasLower bool `json:"has-lower"`
	HasUpper bool `json:"has-upper"`
}

// Range represents chunk range
type Range struct {
	// TODO when Next() generate a chunk, assign the corresponding chunk type
	Type   ChunkType
	ID     int      `json:"id"`
	Bounds []*Bound `json:"bounds"`

	Where string   `json:"where"`
	Args  []string `json:"args"`

	columnOffset map[string]int
	BucketID     int
}

// NewChunkRange return a Range.
func NewChunkRange() *Range {
	return &Range{
		Bounds:       make([]*Bound, 0, 2),
		columnOffset: make(map[string]int),
	}
}

// String returns the string of Range, used for log.
func (c *Range) String() string {
	chunkBytes, err := json.Marshal(c)
	if err != nil {
		log.Warn("fail to encode chunk into string", zap.Error(err))
		return ""
	}

	return string(chunkBytes)
}

func (c *Range) ToString(collation string) (string, []string) {
	if collation != "" {
		collation = fmt.Sprintf(" COLLATE '%s'", collation)
	}

	/* for example:
	there is a bucket in TiDB, and the lowerbound and upperbound are (v1, v3), (v2, v4), and the columns are `a` and `b`,
	this bucket's data range is (a > v1 or (a == v1 and b > v3)) and (a < v2 or (a == v2 and b <= v4))
	*/

	lowerCondition := make([]string, 0, 1)
	upperCondition := make([]string, 0, 1)
	lowerArgs := make([]string, 0, 1)
	upperArgs := make([]string, 0, 1)

	preConditionForLower := make([]string, 0, 1)
	preConditionForUpper := make([]string, 0, 1)
	preConditionArgsForLower := make([]string, 0, 1)
	preConditionArgsForUpper := make([]string, 0, 1)

	for i, bound := range c.Bounds {
		lowerSymbol := gt
		upperSymbol := lt
		if i == len(c.Bounds)-1 {
			upperSymbol = lte
		}

		if bound.HasLower {
			if len(preConditionForLower) > 0 {
				lowerCondition = append(lowerCondition, fmt.Sprintf("(%s AND %s%s %s ?)", strings.Join(preConditionForLower, " AND "), dbutil.ColumnName(bound.Column), collation, lowerSymbol))
				lowerArgs = append(append(lowerArgs, preConditionArgsForLower...), bound.Lower)
			} else {
				lowerCondition = append(lowerCondition, fmt.Sprintf("(%s%s %s ?)", dbutil.ColumnName(bound.Column), collation, lowerSymbol))
				lowerArgs = append(lowerArgs, bound.Lower)
			}
			preConditionForLower = append(preConditionForLower, fmt.Sprintf("%s = ?", dbutil.ColumnName(bound.Column)))
			preConditionArgsForLower = append(preConditionArgsForLower, bound.Lower)
		}

		if bound.HasUpper {
			if len(preConditionForUpper) > 0 {
				upperCondition = append(upperCondition, fmt.Sprintf("(%s AND %s%s %s ?)", strings.Join(preConditionForUpper, " AND "), dbutil.ColumnName(bound.Column), collation, upperSymbol))
				upperArgs = append(append(upperArgs, preConditionArgsForUpper...), bound.Upper)
			} else {
				upperCondition = append(upperCondition, fmt.Sprintf("(%s%s %s ?)", dbutil.ColumnName(bound.Column), collation, upperSymbol))
				upperArgs = append(upperArgs, bound.Upper)
			}
			preConditionForUpper = append(preConditionForUpper, fmt.Sprintf("%s = ?", dbutil.ColumnName(bound.Column)))
			preConditionArgsForUpper = append(preConditionArgsForUpper, bound.Upper)
		}
	}

	if len(upperCondition) == 0 && len(lowerCondition) == 0 {
		return "TRUE", nil
	}

	if len(upperCondition) == 0 {
		return strings.Join(lowerCondition, " OR "), lowerArgs
	}

	if len(lowerCondition) == 0 {
		return strings.Join(upperCondition, " OR "), upperArgs
	}

	return fmt.Sprintf("(%s) AND (%s)", strings.Join(lowerCondition, " OR "), strings.Join(upperCondition, " OR ")), append(lowerArgs, upperArgs...)
}

func (c *Range) addBound(bound *Bound) {
	c.Bounds = append(c.Bounds, bound)
	c.columnOffset[bound.Column] = len(c.Bounds) - 1
}

func (c *Range) updateColumnOffset() {
	c.columnOffset = make(map[string]int)
	for i, bound := range c.Bounds {
		c.columnOffset[bound.Column] = i
	}
}

func (c *Range) Update(column, lower, upper string, updateLower, updateUpper bool) {
	if offset, ok := c.columnOffset[column]; ok {
		// update the bound
		if updateLower {
			c.Bounds[offset].Lower = lower
			c.Bounds[offset].HasLower = true
		}
		if updateUpper {
			c.Bounds[offset].Upper = upper
			c.Bounds[offset].HasUpper = true
		}

		return
	}

	// add a new bound
	c.addBound(&Bound{
		Column:   column,
		Lower:    lower,
		Upper:    upper,
		HasLower: updateLower,
		HasUpper: updateUpper,
	})
}

func (c *Range) Copy() *Range {
	newChunk := NewChunkRange()
	for _, bound := range c.Bounds {
		newChunk.addBound(&Bound{
			Column:   bound.Column,
			Lower:    bound.Lower,
			Upper:    bound.Upper,
			HasLower: bound.HasLower,
			HasUpper: bound.HasUpper,
		})
	}

	return newChunk
}

func (c *Range) copyAndUpdate(column, lower, upper string, updateLower, updateUpper bool) *Range {
	newChunk := c.Copy()
	newChunk.Update(column, lower, upper, updateLower, updateUpper)
	return newChunk
}

func InitChunks(chunks []*Range, chunkBeginID, bucketID int, collation, limits string) int {
	if chunks == nil {
		return chunkBeginID
	}
	for _, chunk := range chunks {
		conditions, args := chunk.ToString(collation)
		chunk.ID = chunkBeginID
		chunkBeginID++
		chunk.Where = fmt.Sprintf("((%s) AND %s)", conditions, limits)
		chunk.Args = args
		chunk.BucketID = bucketID
	}
	return chunkBeginID
}