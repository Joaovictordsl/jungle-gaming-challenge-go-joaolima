package money

type Money struct {
	cents    int64
	currency string
}

func (m Money) MinorUnits() int64 {
	return m.cents
}

func (m Money) Currency() string {
	return m.currency
}
