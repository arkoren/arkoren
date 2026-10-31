package schema

import (
	"errors"
	"fmt"
	"slices"
)

// tableValidation tracks the cross-action invariants of one detached table.
// It visits actions in declaration order so diagnostics remain deterministic;
// added includes later columns so CREATE indexes can use forward references.
type tableValidation struct {
	// table is the private snapshot being checked, never an application draft.
	table *Table

	// added contains every declared column, including those not visited yet.
	added []string

	// seen contains visited columns for declaration-order duplicate detection.
	seen []string

	// claimed prevents two created constraints from sharing a generated name.
	claimed map[string]bool

	// primary retains the sole table-level primary-key declaration.
	primary []string

	// autoIncrements counts visited identity columns for final key validation.
	autoIncrements int
}

// addedColumns collects names without validating them or changing action order.
// Individual setting errors have already been retained on the table; action
// validation still decides which combined-declaration error is reported first.
func (table *Table) addedColumns() []string {
	var columns []string

	for _, declaration := range table.actions {
		if declaration.kind == actionAddColumn {
			columns = append(columns, declaration.column.name)
		}
	}

	return columns
}

// check validates one action and records only its cross-action bookkeeping.
// Dropped constraints validate generated names without claiming them, allowing
// a declaration to add and drop the same constraint in written order.
func (validation *tableValidation) check(declaration action) error {
	switch declaration.kind {
	case actionAddColumn:
		return validation.checkColumn(declaration.column)
	case actionPrimary:
		return validation.setPrimary(declaration.names)
	case actionUnique, actionIndex, actionDropIndex, actionDropUnique:
		return validation.checkIndex(declaration)
	case actionDropColumn, actionRenameColumn:
		return validation.table.checkNames(declaration.names)
	case actionDropForeign:
		return validation.checkDroppedForeign(declaration.names)
	case actionChange:
		if err := validation.table.checkNames([]string{declaration.change.column.name}); err != nil {
			return err
		}

		return declaration.change.validate()
	case actionForeign:
		if err := validation.table.checkNames([]string{declaration.foreign.column}); err != nil {
			return err
		}

		return validation.claim([]string{declaration.foreign.column}, "fkey")
	}

	return nil
}

// checkColumn checks one column before publishing its key contributions.
// Column-level unique and foreign constraints share the same name registry as
// table-level declarations, preventing ambiguous generated DDL identifiers.
func (validation *tableValidation) checkColumn(column *Column) error {
	if slices.Contains(validation.seen, column.name) {
		return fmt.Errorf("duplicate column %q", column.name)
	}

	validation.seen = append(validation.seen, column.name)

	if err := column.validate(); err != nil {
		return err
	}

	if column.autoIncrement {
		validation.autoIncrements++
	}

	if column.primary && !column.autoIncrement {
		validation.table.primary = append(validation.table.primary, column.name)
	}

	if column.unique {
		if err := validation.claim([]string{column.name}, "key"); err != nil {
			return err
		}
	}

	if column.references {
		return validation.claim([]string{column.name}, "fkey")
	}

	return nil
}

// setPrimary retains one nonempty table-level key for final combination with
// column modifiers. It does not inspect columns until all actions are checked.
func (validation *tableValidation) setPrimary(columns []string) error {
	if len(columns) == 0 || validation.primary != nil {
		return errors.New("primary key needs columns and can be set once")
	}

	validation.primary = columns

	return nil
}

// checkIndex checks column membership and the generated index identifier.
// Only creation claims names; dropping an existing index need not be unique
// within the declaration and does not require knowing the existing schema.
func (validation *tableValidation) checkIndex(declaration action) error {
	suffix := "key"

	switch declaration.kind {
	case actionIndex, actionDropIndex:
		suffix = "index"
	}

	if err := validation.table.checkKey(declaration.names, validation.added); err != nil {
		return err
	}

	if declaration.kind == actionUnique || declaration.kind == actionIndex {
		return validation.claim(declaration.names, suffix)
	}

	_, err := constraintName(validation.table.name, declaration.names, suffix)

	return err
}

// checkDroppedForeign validates both the source column and the generated
// constraint identifier at construction, before lazy rendering can begin.
func (validation *tableValidation) checkDroppedForeign(columns []string) error {
	if err := validation.table.checkNames(columns); err != nil {
		return err
	}

	_, err := constraintName(validation.table.name, columns, "fkey")

	return err
}

// claim reserves a generated constraint name after checking its byte limit.
// The registry belongs only to this validation pass; no mutable state escapes
// into the published operation or application-retained declaration pointers.
func (validation *tableValidation) claim(columns []string, suffix string) error {
	name, err := constraintName(validation.table.name, columns, suffix)

	if err != nil {
		return err
	}

	if validation.claimed[name] {
		return fmt.Errorf("constraint name %q is generated twice", name)
	}

	validation.claimed[name] = true

	return nil
}
