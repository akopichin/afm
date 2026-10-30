package jsonstrict

import (
	"encoding/json"
	"errors"
	"math/big"
)

// Integer accepts integral JSON numbers regardless of decimal or exponent spelling.
type Integer int

func (i *Integer) UnmarshalJSON(raw []byte) error {
	var number json.Number
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return errors.New("expected a JSON integer")
	}
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	// Enough precision for every input digit prevents a fractional value rounding to an integer.
	value, _, err := big.ParseFloat(number.String(), 10, uint(len(raw)*4+64), big.ToNearestEven)
	if err != nil {
		return err
	}
	n, accuracy := value.Int64()
	if accuracy != big.Exact || int64(int(n)) != n {
		return errors.New("expected an integer in range")
	}
	*i = Integer(n)
	return nil
}
