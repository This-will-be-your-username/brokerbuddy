# Test data

`pdf/` holds **synthetic** bank statements (all names, merchants and amounts are made up) used to test the PDF importer, each with an `*.expected.json` listing the transactions it contains.

| File | Layout it exercises |
|------|---------------------|
| `bank_a` | Debit / Credit / Balance columns, dd/mm/yyyy, two-line descriptions, header repeated on page 2, opening and closing balance rows, footer text |
| `bank_b` | One Amount column with `CR`/`DR` markers, dates with no year (taken from the statement period) |
| `card_c` | Credit card: one signed amount column, no balance, `05 Oct 25` dates |
| `nohdr_d` | No header row at all; ISO dates; signs learned from the running balance |
| `reverse_e` | Newest first, one *unsigned* amount column: direction comes from the balance |
| `daily_f` | Date printed once per day, headings wrapped over three lines ("Money" / "out"), `$` amounts |
| `twodate_g` | Two date columns (transaction and posted date) |
| `mono_h` | Monospace text-style statement: columns are runs of spaces inside one string |
| `scan_i` | An image-only PDF (must be rejected with a clear message) |

Regenerate with `node testdata/gen_pdfs.js` (needs Edge or Chrome installed; it prints each PDF to a file).
