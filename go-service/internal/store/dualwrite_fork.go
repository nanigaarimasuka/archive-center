package store

import (
	"context"
)

func (d *dualWriteStore) WithdrawMemoryVectorOperations(ctx context.Context, operationKeys []string, reason string) (int, error) {
	if primary, ok := d.primary.(MemoryVectorOperationWithdrawer); ok {
		return primary.WithdrawMemoryVectorOperations(ctx, operationKeys, reason)
	}
	if shadow, ok := d.shadow.(MemoryVectorOperationWithdrawer); ok {
		return shadow.WithdrawMemoryVectorOperations(ctx, operationKeys, reason)
	}
	return 0, ErrNotEnabled
}

func (d *dualWriteStore) CountMemoryVectorOperationStates(ctx context.Context, operationKeys []string) (map[string]int, error) {
	if primary, ok := d.primary.(MemoryVectorOperationStateCounter); ok {
		return primary.CountMemoryVectorOperationStates(ctx, operationKeys)
	}
	if shadow, ok := d.shadow.(MemoryVectorOperationStateCounter); ok {
		return shadow.CountMemoryVectorOperationStates(ctx, operationKeys)
	}
	return nil, ErrNotEnabled
}
