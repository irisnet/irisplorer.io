package task

import (
	"github.com/irisnet/explorer/backend/orm/document"
	"github.com/irisnet/explorer/backend/logger"
	"gopkg.in/mgo.v2/txn"
	"gopkg.in/mgo.v2/bson"
	"time"
	"github.com/irisnet/explorer/backend/types"
	"fmt"
	"github.com/irisnet/explorer/backend/utils"
	"math"
	"strings"
	"github.com/irisnet/explorer/backend/lcd"
	"github.com/irisnet/explorer/backend/conf"
)

type StaticDelegatorByMonthTask struct {
	mStaticModel           document.ExStaticDelegatorMonth
	staticModel            document.ExStaticDelegator
	txModel                document.CommonTx
	AddressCoin            map[string]document.Coin
	AddrPeriodCommission   map[string]document.Coin
	AddrTerminalCommission map[string]document.Coin
	AddrBeginCommission    map[string]document.Coin
}

func (task *StaticDelegatorByMonthTask) Name() string {
	return "static_delegator_month"
}
func (task *StaticDelegatorByMonthTask) Start() {
	taskName := task.Name()
	timeInterval := 3600 * 24 * 30

	if err := tcService.runTask(taskName, timeInterval, task.DoTask); err != nil {
		logger.Error(err.Error())
	}
}

func (task *StaticDelegatorByMonthTask) DoTask() error {
	terminaldate, err := task.staticModel.Terminaldate()
	if err != nil {
		return err
	}

	terminalData, err := task.staticModel.GetDataByDate(terminaldate)
	if err != nil {
		logger.Error("Get GetData ByDate fail",
			logger.String("date", terminaldate.String()),
			logger.String("err", err.Error()))
		return err
	}
	res := make([]document.ExStaticDelegatorMonth, 0, len(terminalData))

	txs, err := task.getPeriodTxByAddress(terminaldate.Year(), int(terminaldate.Month()), "") //all address txs
	if err != nil {
		return err
	}

	for _, val := range terminalData {
		one := task.getStaticDelegator(val, txs)
		res = append(res, one)
	}

	if len(res) == 0 {
		return nil
	}
	return task.mStaticModel.Batch(task.saveOps(res))
}

func parseCoinAmountAndUnitFromStr(s string) (document.Coin) {
	for _, denom := range rewardsDenom {
		if strings.HasSuffix(s, denom) {
			coin := utils.ParseCoin(s)
			return document.Coin{
				Denom:  coin.Denom,
				Amount: coin.Amount,
			}
		}
	}
	return document.Coin{}
}

func (task *StaticDelegatorByMonthTask) getStaticDelegator(terminalval document.ExStaticDelegator, txs []document.CommonTx) document.ExStaticDelegatorMonth {
	periodRewards := task.getPeriodRewards(terminalval)
	datestr := fmt.Sprintf("%d-%02d-01T00:00:00", terminalval.Date.Year(), terminalval.Date.Month())
	date, _ := time.ParseInLocation(types.TimeLayout, datestr, cstZone)
	delagation, err := task.staticModel.GetDataOneDay(date, terminalval.Address)
	if err != nil {
		logger.Error("get DelegationData failed",
			logger.String("func", "get StaticDelegator"),
			logger.String("err", err.Error()))
	}

	if task.AddrBeginCommission == nil {
		task.AddrBeginCommission = make(map[string]document.Coin)
	}
	if task.AddrTerminalCommission == nil {
		task.AddrTerminalCommission = make(map[string]document.Coin)
	}
	if len(delagation.Commission) > 0 {
		task.AddrBeginCommission[delagation.Address] = document.Coin{
			Amount: delagation.Commission[0].Iris * math.Pow10(18),
			Denom:  types.IRISAttoUint,
		}
	}
	if len(terminalval.Commission) > 0 {
		task.AddrTerminalCommission[terminalval.Address] = document.Coin{
			Amount: terminalval.Commission[0].Iris * math.Pow10(18),
			Denom:  types.IRISAttoUint,
		}
	}

	incrementRewardsPart := task.getIncrementRewards(terminalval, delagation, periodRewards)

	item := document.ExStaticDelegatorMonth{
		Address:                terminalval.Address,
		Date:                   fmt.Sprintf("%d.%02d", terminalval.Date.Year(), terminalval.Date.Month()),
		TerminalDelegation:     document.Coin{Denom: terminalval.Delegation.Denom, Amount: terminalval.Delegation.Amount},
		PeriodDelegationTimes:  task.getPeriodDelegationTimes(txs),
		PeriodWithdrawRewards:  periodRewards,
		IncrementDelegation:    task.getIncrementDelegation(terminalval, delagation),
		PeriodIncrementRewards: incrementRewardsPart,
	}
	if len(terminalval.DelegationsRewards) > 0 {
		item.TerminalRewards = terminalval.DelegationsRewards[0]
	}

	return item
}
func (task *StaticDelegatorByMonthTask) getPeriodTxByAddress(year, month int, address string) ([]document.CommonTx, error) {
	//fmt.Println(year,month)
	//fmt.Println(fmt.Sprintf("%d-%02d-01T00:00:00", year, month))
	//fmt.Println(fmt.Sprintf("%d-%02d-01T00:00:00", year, month+1))
	starttime, _ := time.ParseInLocation(types.TimeLayout, fmt.Sprintf("%d-%02d-01T00:00:00", year, month), cstZone)
	endtime, _ := time.ParseInLocation(types.TimeLayout, fmt.Sprintf("%d-%02d-01T00:00:00", year, month+1), cstZone)
	txs, err := task.txModel.GetTxsByDurationAddress(starttime, endtime, address)
	if err != nil {
		return nil, err
	}
	for _, tx := range txs {
		switch tx.Type {
		case types.TxTypeWithdrawDelegatorReward, types.TxTypeWithdrawDelegatorRewardsAll, types.TxTypeWithdrawValidatorRewardsAll,
			types.TxTypeBeginRedelegate, types.TxTypeStakeBeginUnbonding, types.TxTypeStakeDelegate:
			task.getCoinflowByHash(tx.TxHash)
		}
	}
	return txs, nil
}

func (task *StaticDelegatorByMonthTask) getPeriodRewards(terminal document.ExStaticDelegator) document.Rewards {
	var rewards document.Rewards
	if data, ok := task.AddressCoin[terminal.Address]; ok {
		rewards.Iris += data.Amount / math.Pow10(18)
	}
	return rewards
}

func (task *StaticDelegatorByMonthTask) getPeriodDelegationTimes(txs []document.CommonTx) (total int) {
	for _, val := range txs {
		if val.Type == types.TxTypeStakeDelegate ||
			val.Type == types.TxTypeBeginRedelegate ||
			val.Type == types.TxTypeStakeBeginUnbonding {
			total++
		}
	}
	return
}

func (task *StaticDelegatorByMonthTask) getIncrementRewards(terminal, delagation document.ExStaticDelegator, periodRewards document.Rewards) (document.Rewards) {
	var rewards document.Rewards
	var irisatto float64
	if len(terminal.DelegationsRewards) > 0 {
		rewards.Iris = terminal.DelegationsRewards[0].Iris
		irisatto = rewards.Iris * math.Pow10(18)
	}

	if len(delagation.DelegationsRewards) > 0 {
		rewards.Iris -= delagation.DelegationsRewards[0].Iris
		irisatto -= rewards.Iris * math.Pow10(18)
	}

	irisatto += periodRewards.Iris * math.Pow10(18)
	rewards.Iris += periodRewards.Iris
	rewards.IrisAtto = fmt.Sprint(irisatto)
	return rewards
}

func (task *StaticDelegatorByMonthTask) getCoinflowByHash(txhash string) {
	result := lcd.BlockCoinFlow(txhash)
	if length := len(result.CoinFlow); length > 0 {
		if task.AddressCoin == nil {
			task.AddressCoin = make(map[string]document.Coin, length)
		}
		if task.AddrPeriodCommission == nil {
			task.AddrPeriodCommission = make(map[string]document.Coin, length)
		}
		for _, val := range result.CoinFlow {
			values := strings.Split(val, "::")
			if len(values) != 6 {
				continue
			}
			if strings.HasPrefix(values[3], types.DelegatorRewardTag) {
				task.AddressCoin[values[1]] = parseCoinAmountAndUnitFromStr(values[2])
			} else if strings.HasPrefix(values[3], types.ValidatorRewardTag) {
				address := utils.Convert(conf.Get().Hub.Prefix.AccAddr, values[0])
				task.AddressCoin[address] = parseCoinAmountAndUnitFromStr(values[2])
			} else if strings.HasPrefix(values[3], types.ValidatorCommissionTag) {
				address := utils.Convert(conf.Get().Hub.Prefix.AccAddr, values[0])
				task.AddrPeriodCommission[address] = parseCoinAmountAndUnitFromStr(values[2])
			}
		}

	}
	return
}

func (task *StaticDelegatorByMonthTask) getIncrementDelegation(terminal, delagation document.ExStaticDelegator) document.Coin {
	amount := terminal.Delegation.Amount - delagation.Delegation.Amount
	return document.Coin{
		Denom:  terminal.Delegation.Denom,
		Amount: amount,
	}

}

//caculatetime format [ 2020-04-03T00:00:00 ]
func (task *StaticDelegatorByMonthTask) getDelegationData(caculatetime string) ([]document.ExStaticDelegator, error) {
	date, err := time.ParseInLocation(types.TimeLayout, caculatetime, cstZone)
	if err != nil {
		return nil, err
	}
	data, err := task.staticModel.GetDataByDate(date)
	if err != nil {
		logger.Error("Get GetData ByDate fail",
			logger.String("date", date.String()),
			logger.String("err", err.Error()))
		return nil, err
	}
	return data, nil
}

func (task *StaticDelegatorByMonthTask) saveOps(datas []document.ExStaticDelegatorMonth) ([]txn.Op) {
	var ops = make([]txn.Op, 0, len(datas))
	for _, val := range datas {
		val.Id = bson.NewObjectId()
		val.CreateAt = time.Now().Unix()
		val.UpdateAt = time.Now().Unix()
		op := txn.Op{
			C:      task.mStaticModel.Name(),
			Id:     bson.NewObjectId(),
			Insert: val,
		}
		ops = append(ops, op)
	}
	return ops
}