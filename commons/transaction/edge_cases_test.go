package transaction

import (
	"math"
	"testing"

	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/stretchr/testify/assert"
)

func TestEdgeCasesAndBoundaries(t *testing.T) {
	t.Run("Scale with extreme values", func(t *testing.T) {
		// Test with very large scale differences
		result := Scale(1, 0, 15)
		assert.Equal(t, int64(1000000000000000), result)

		// Test with negative scale (scaling down)
		result = Scale(1000000000000000, 15, 0)
		assert.Equal(t, int64(1), result)
	})

	t.Run("FindScale with very small decimals", func(t *testing.T) {
		amount := FindScale("BTC", 0.00000001, 8) // 1 satoshi
		assert.Equal(t, int64(1), amount.Value)
		assert.Equal(t, int64(8), amount.Scale)
	})

	t.Run("Normalize with extreme scale differences", func(t *testing.T) {
		total := Amount{Value: 1, Scale: 0}
		amount := Amount{Value: 1, Scale: 18}
		remaining := Amount{Value: 1000000000000000000, Scale: 18}

		Normalize(&total, &amount, &remaining)

		assert.Equal(t, int64(1000000000000000001), total.Value)
		assert.Equal(t, int64(18), total.Scale)
		assert.Equal(t, int64(999999999999999999), remaining.Value)
	})

	t.Run("OperateBalances with max values", func(t *testing.T) {
		// Test adding 1 to max value - 1
		amount := Amount{Value: 1, Scale: 0}
		balance := Balance{Available: math.MaxInt64 - 1, Scale: 0}

		result, err := OperateBalances(amount, balance, constant.CREDIT)
		assert.NoError(t, err)
		assert.Equal(t, int64(math.MaxInt64), result.Available)

		// Test adding 1 more should overflow
		balance.Available = math.MaxInt64
		_, err = OperateBalances(amount, balance, constant.CREDIT)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), constant.ErrOverFlowInt64.Error())
	})

	t.Run("CalculateTotal with zero percentage", func(t *testing.T) {
		fromTos := []FromTo{
			{Account: "0#acc1", Share: &Share{Percentage: 0}},
		}
		send := Send{Asset: "USD", Value: 10000, Scale: 2}

		totalChan := make(chan int64, 1)
		fromToChan := make(chan map[string]Amount, 1)
		scdtChan := make(chan []string, 1)

		go CalculateTotal(fromTos, send, totalChan, fromToChan, scdtChan)

		gotTotal := <-totalChan
		gotFromTo := <-fromToChan
		<-scdtChan

		assert.Equal(t, int64(0), gotTotal)
		assert.Equal(t, int64(0), gotFromTo["0#acc1"].Value)
	})
}
