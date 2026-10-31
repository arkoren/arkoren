package statement

import (
	"cmp"
	"slices"

	"arkoren.dev/core/service/search"
	"arkoren.dev/foundation/query"
)

// Reindex compiles the tsvector refresh for one logical index. Table names are
// quoted independently of values; the caller owns the surrounding transaction.
func Reindex(documents string, schema search.Schema) query.RawStatement {
	reindex := &statement{}

	reindex.write("UPDATE " + query.Postgres.Quote(documents) + " SET tsv = ")
	writeTSVector(reindex, "source", schema.Searchable)
	reindex.write(" WHERE index_name = ?", schema.Name)

	return reindex.raw()
}

// Put compiles one bounded upsert chunk, retaining the definition-row share
// lock that prevents a concurrent index drop from racing document publication.
func Put(documents, indexes string, schema search.Schema, docs []search.Document) query.RawStatement {
	s := &statement{}

	s.write("INSERT INTO " + query.Postgres.Quote(documents) + " (index_name, id, source, tsv) SELECT i.name, v.id, v.source, ")
	writeTSVector(s, "v.source", schema.Searchable)
	s.write(" FROM " + query.Postgres.Quote(indexes) + " i, (VALUES ")
	writeDocumentValues(s, docs)
	s.write(") AS v(id, source) WHERE i.name = ? FOR SHARE OF i"+
		" ON CONFLICT (index_name, id) DO UPDATE SET source = EXCLUDED.source, tsv = EXCLUDED.tsv", schema.Name)

	return s.raw()
}

// Search compiles the ordered window and capped count from the same protected
// filter expression. It binds values in their original order and preserves the
// first invalid comparison before either statement can reach the database.
func Search(
	documents, name string,
	spec search.Spec,
	limit, offset int,
) (query.RawStatement, query.RawStatement, error) {
	where := &statement{}

	where.write("index_name = ?", name)

	if len(spec.Text) > 0 {
		where.write(" AND tsv @@ (")
		writeTSQuery(where, spec.Text)
		where.write(")")
	}

	if len(spec.Where) > 0 {
		where.write(" AND (")
		writeConditions(where, spec.Where)
		where.write(")")
	}

	selection := &statement{}

	if len(spec.Text) > 0 {
		selection.write("SELECT id, source::text AS doc, ts_rank(tsv, (")
		writeTSQuery(selection, spec.Text)
		selection.write("))::float8 AS score")
	} else {
		selection.write("SELECT id, source::text AS doc, 0::float8 AS score")
	}

	selection.write(" FROM "+query.Postgres.Quote(documents)+" WHERE "+where.sql.String(), where.args...)
	selection.write(" ORDER BY ")

	for _, order := range spec.Orders {
		writeOrder(selection, order)
	}

	if len(spec.Text) > 0 {
		selection.write("score DESC, ")
	}

	selection.write(`id COLLATE "C" LIMIT ? OFFSET ?`, limit, offset)

	if err := cmp.Or(where.err, selection.err); err != nil {
		return query.RawStatement{}, query.RawStatement{}, err
	}

	counting := &statement{}

	counting.write("SELECT count(*) FROM (SELECT 1 FROM "+query.Postgres.Quote(documents)+" WHERE "+where.sql.String()+" LIMIT ?) AS capped",
		append(slices.Clone(where.args), search.MaxHits)...)

	return selection.raw(), counting.raw(), nil
}
