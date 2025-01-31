package tx_parser

import (
	"encoding/binary"
	"fmt"

	"github.com/gagliardetto/solana-go"
)

// RaydiumParser handles parsing Raydium protocol swaps
type RaydiumParser struct{}

// NewRaydiumParser creates a new Raydium parser instance
func NewRaydiumParser() *RaydiumParser {
	return &RaydiumParser{}
}

// CanHandle checks if this parser can handle the given instruction
func (p *RaydiumParser) CanHandle(instruction solana.CompiledInstruction, accountKeys []solana.PublicKey) bool {
	programID := accountKeys[instruction.ProgramIDIndex]
	return isRaydiumProgram(programID)
}

// ParseInstruction processes the Raydium instruction and returns swap information
func (p *RaydiumParser) ParseInstruction(instruction solana.CompiledInstruction, instructionIndex int, ctx *TransactionContext) ([]*SwapInfo, error) {
	var swaps []*SwapInfo

	// Process transfers in each group of inner instructions
	for _, innerSet := range ctx.Meta.InnerInstructions {
		if innerSet.Index == uint16(instructionIndex) {
			var currentTransfers []TokenInfo
			for _, innerInstr := range innerSet.Instructions {
				var transfer *TokenInfo
				var err error

				switch {
				case isRaydiumTransfer(innerInstr, ctx.AccountKeys):
					transfer, err = p.processTransfer(innerInstr, ctx)
				case isRaydiumTransferChecked(innerInstr, ctx.AccountKeys):
					transfer, err = p.processTransferChecked(innerInstr, ctx)
				}

				if err != nil || transfer == nil {
					continue
				}

				currentTransfers = append(currentTransfers, *transfer)

				// When we have a pair of transfers, build a swap
				if len(currentTransfers) == 2 {
					swap, err := p.buildSwapInfo(currentTransfers[0], currentTransfers[1], ctx)
					if err != nil {
						// Reset for next pair
						currentTransfers = currentTransfers[2:]
						continue
					}
					swaps = append(swaps, swap)
					// Reset for next pair but keep any remaining transfers
					currentTransfers = currentTransfers[2:]
				}
			}
		}
	}

	if len(swaps) == 0 {
		return nil, fmt.Errorf("no valid Raydium swaps found")
	}

	return swaps, nil
}

// isRaydiumTransfer checks if the instruction is a token transfer
func isRaydiumTransfer(instr solana.CompiledInstruction, accountKeys []solana.PublicKey) bool {
	if len(instr.Accounts) < 3 || len(instr.Data) < 9 {
		return false
	}

	progID := accountKeys[instr.ProgramIDIndex]
	if !progID.Equals(solana.TokenProgramID) {
		return false
	}

	return instr.Data[0] == 3 // Transfer instruction
}

// isRaydiumTransferChecked checks if the instruction is a token transfer with amount check
func isRaydiumTransferChecked(instr solana.CompiledInstruction, accountKeys []solana.PublicKey) bool {
	if len(instr.Accounts) < 4 || len(instr.Data) < 9 {
		return false
	}

	progID := accountKeys[instr.ProgramIDIndex]
	if !progID.Equals(solana.TokenProgramID) && !progID.Equals(solana.Token2022ProgramID) {
		return false
	}

	return instr.Data[0] == 12 // TransferChecked instruction
}

// processTransfer handles regular token transfers
func (p *RaydiumParser) processTransfer(instr solana.CompiledInstruction, ctx *TransactionContext) (*TokenInfo, error) {
	if len(instr.Data) < 9 {
		return nil, fmt.Errorf("invalid transfer instruction data")
	}

	amount := binary.LittleEndian.Uint64(instr.Data[1:9])

	// Get source and destination accounts
	sourceAcc := ctx.AccountKeys[instr.Accounts[0]]
	destAcc := ctx.AccountKeys[instr.Accounts[1]]

	// Find token mint from either source or destination account
	mint := p.findTokenMint(sourceAcc, destAcc, ctx)
	if mint == (solana.PublicKey{}) {
		return nil, fmt.Errorf("could not determine token mint")
	}

	return &TokenInfo{
		Mint:     mint,
		Amount:   amount,
		Decimals: ctx.GetMintDecimals(mint),
	}, nil
}

// processTransferChecked handles checked token transfers
func (p *RaydiumParser) processTransferChecked(instr solana.CompiledInstruction, ctx *TransactionContext) (*TokenInfo, error) {
	if len(instr.Data) < 9 {
		return nil, fmt.Errorf("invalid transfer checked instruction data")
	}

	amount := binary.LittleEndian.Uint64(instr.Data[1:9])
	mint := ctx.AccountKeys[instr.Accounts[1]]

	return &TokenInfo{
		Mint:     mint,
		Amount:   amount,
		Decimals: ctx.GetMintDecimals(mint),
	}, nil
}

// findTokenMint looks up the mint for token accounts
func (p *RaydiumParser) findTokenMint(source, dest solana.PublicKey, ctx *TransactionContext) solana.PublicKey {
	// Check both pre and post token balances
	balances := append(ctx.Meta.PreTokenBalances, ctx.Meta.PostTokenBalances...)

	for _, balance := range balances {
		accKey := ctx.AccountKeys[balance.AccountIndex]
		if accKey.Equals(source) || accKey.Equals(dest) {
			return balance.Mint
		}
	}

	return solana.PublicKey{}
}

// buildSwapInfo creates a SwapInfo from a pair of transfers
func (p *RaydiumParser) buildSwapInfo(transfer1, transfer2 TokenInfo, ctx *TransactionContext) (*SwapInfo, error) {
	if transfer1.Mint.Equals(transfer2.Mint) {
		return nil, fmt.Errorf("same token in both transfers")
	}

	swapInfo := &SwapInfo{
		Protocol: SwapTypeRaydium,
	}

	// Find input token (transferred from signer)
	signers := ctx.Transaction.Message.Signers()
	found := false

	// Check first transfer
	for _, balance := range ctx.Meta.PreTokenBalances {
		if balance.Mint.Equals(transfer1.Mint) {
			owner := balance.Owner
			for _, signer := range signers {
				if owner.Equals(signer) {
					swapInfo.TokenIn = transfer1
					swapInfo.TokenOut = transfer2
					found = true
					break
				}
			}
			break
		}
	}

	// If first transfer wasn't from signer, use second transfer
	if !found {
		swapInfo.TokenIn = transfer2
		swapInfo.TokenOut = transfer1
	}

	return swapInfo, nil
}
