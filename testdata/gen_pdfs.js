// Generates synthetic bank-statement PDFs (all data is made up) with headless Edge/Chrome, plus
// *.expected.json files describing the transactions each PDF contains. Run: node testdata/gen_pdfs.js
const fs = require("fs"), path = require("path"), cp = require("child_process");
const out = path.join(__dirname, "pdf");
const browsers = ["C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe", "C:/Program Files/Google/Chrome/Application/chrome.exe"];
const browser = browsers.find(fs.existsSync);
if (!browser) throw new Error("no Edge/Chrome found");

const css = `<style>@page{size:A4;margin:14mm}body{font:9pt Arial,sans-serif;color:#111}h1{font-size:14pt;margin:0 0 4px}
table{width:100%;border-collapse:collapse}thead{display:table-header-group}th{text-align:left;border-bottom:1px solid #333;padding:3px 4px}
td{padding:3px 4px;vertical-align:top}.r{text-align:right}.meta{margin:0 0 10px;color:#444}.foot{margin-top:14px;font-size:8pt;color:#555}</style>`;
const fmt = n => n.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const merchants = [
  ["WOOLWORTHS 1234 SYDNEY NSW", "Card purchase", 85.4], ["STARBUCKS 22 NEWTOWN", "Visa purchase", 6.5], ["AGL ENERGY", "Direct debit", 142], ["TELSTRA", "Direct debit", 89],
  ["BUNNINGS 7788 PENRITH", "Eftpos purchase", 63.15], ["NETFLIX.COM", "Visa purchase", 22.99], ["UBER *TRIP", "Visa purchase", 18.2], ["CHEMIST WAREHOUSE", "Eftpos purchase", 34.1],
  ["ALDI STORES 88", "Eftpos purchase", 96.3], ["SPOTIFY", "Visa purchase", 13.99], ["BP 5566 PARRAMATTA", "Eftpos purchase", 72], ["ATM WITHDRAWAL KING ST", "Cash", 200],
];
function make(n, startBal, seed) { // deterministic list of {day, desc, sub, debit, credit}
  const rows = []; let bal = startBal, s = seed;
  const rnd = () => (s = (s * 9301 + 49297) % 233280) / 233280;
  for (let i = 0; i < n; i++) {
    const day = 1 + Math.floor(i * 29 / n);
    if (i % 9 === 0) { const c = 2000 + Math.round(rnd() * 400); bal += c; rows.push({ day, desc: "SALARY ACME PTY LTD", sub: "Direct credit", credit: c, bal }); continue; }
    const m = merchants[Math.floor(rnd() * merchants.length)], d = Math.round((m[2] * (0.8 + rnd() * 0.5)) * 100) / 100;
    bal = Math.round((bal - d) * 100) / 100; rows.push({ day, desc: m[0], sub: m[1], debit: d, bal });
  }
  return rows;
}
const mon = ["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"], p2 = n => String(n).padStart(2, "0");
const files = {};

// A: debit/credit/balance columns, dd/mm/yyyy, two-line descriptions, header repeats across pages
{ const rows = make(46, 3200.5, 1);
  const body = rows.map(r => `<tr><td>${p2(r.day)}/10/2025</td><td>${r.desc}${r.sub ? "<br>" + r.sub : ""}</td><td class=r>${r.debit ? fmt(r.debit) : ""}</td><td class=r>${r.credit ? fmt(r.credit) : ""}</td><td class=r>${fmt(r.bal)}</td></tr>`).join("");
  files.bank_a = { html: `${css}<h1>Everyday Account</h1><p class=meta>Statement period 1 Oct 2025 - 31 Oct 2025 &nbsp; BSB 000-000 Account 00000000</p>
  <table><thead><tr><th>Date</th><th>Transaction details</th><th class=r>Debit</th><th class=r>Credit</th><th class=r>Balance</th></tr></thead><tbody>
  <tr><td>01/10/2025</td><td>OPENING BALANCE</td><td></td><td></td><td class=r>3,200.50</td></tr>${body}
  <tr><td>31/10/2025</td><td>CLOSING BALANCE</td><td></td><td></td><td class=r>${fmt(rows[rows.length - 1].bal)}</td></tr></tbody></table><p class=foot>Total debits and credits shown above. Page footer text 1234.</p>`,
    expected: rows.map(r => ({ date: `2025-10-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }

// B: single Amount column with CR/DR markers, dates without a year (year comes from the period line)
{ const rows = make(20, 1500, 2);
  const body = rows.map(r => `<tr><td>${p2(r.day)} Nov</td><td>${r.desc}</td><td class=r>${fmt(r.debit || r.credit)} ${r.debit ? "DR" : "CR"}</td><td class=r>${fmt(r.bal)} CR</td></tr>`).join("");
  files.bank_b = { html: `${css}<h1>Savings Maximiser</h1><p class=meta>Statement from 1 November 2025 to 30 November 2025</p>
  <table><thead><tr><th>Date</th><th>Description</th><th class=r>Amount</th><th class=r>Balance</th></tr></thead><tbody>${body}</tbody></table>`,
    expected: rows.map(r => ({ date: `2025-11-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }

// C: credit card, one signed amount column (purchases positive, payments negative), no balance, "05 Oct 25" dates
{ const rows = make(24, 0, 3); rows.forEach((r, i) => { if (r.credit || i % 11 === 10) { r.debit = 0; r.credit = 400; r.desc = "PAYMENT RECEIVED - THANK YOU"; } });
const body = rows.map(r => `<tr><td>${p2(r.day)} Oct 25</td><td>${r.desc}</td><td class=r>${r.credit ? "-" + fmt(r.credit) : fmt(r.debit)}</td></tr>`).join("");
  files.card_c = { html: `${css}<h1>Rewards Card</h1><p class=meta>Statement closing date 31 Oct 2025</p>
  <table><thead><tr><th>Trans date</th><th>Details</th><th class=r>Amount ($)</th></tr></thead><tbody>${body}</tbody></table>`,
    expected: rows.map(r => ({ date: `2025-10-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }

// D: no header row at all: ISO dates, then description, amount and balance
{ const rows = make(16, 900, 4);
  const body = rows.map(r => `<tr><td>2025-12-${p2(r.day)}</td><td>${r.desc}</td><td class=r>${r.debit ? "-" + fmt(r.debit) : fmt(r.credit)}</td><td class=r>${fmt(r.bal)}</td></tr>`).join("");
  files.nohdr_d = { html: `${css}<h1>Transaction history</h1><table><tbody>${body}</tbody></table>`,
    expected: rows.map(r => ({ date: `2025-12-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }

// E: newest first, a single UNSIGNED amount column and a balance: direction must come from the balance
{ const rows = make(18, 2500, 5).reverse();
  const body = rows.map(r => `<tr><td>${p2(r.day)} Sep 2025</td><td>${r.desc}</td><td class=r>${fmt(r.debit || r.credit)}</td><td class=r>${fmt(r.bal)}</td></tr>`).join("");
  const first = rows[rows.length - 1], openBal = first.debit ? Math.round((first.bal + first.debit) * 100) / 100 : Math.round((first.bal - first.credit) * 100) / 100;
  files.reverse_e = { html: `${css}<h1>Cheque Account</h1><p class=meta>Period 1 Sep 2025 to 30 Sep 2025</p>
  <table><thead><tr><th>Date</th><th>Details</th><th class=r>Amount</th><th class=r>Balance</th></tr></thead><tbody>${body}
  <tr><td>01 Sep 2025</td><td>Opening balance</td><td></td><td class=r>${fmt(openBal)}</td></tr></tbody></table>`,
    expected: rows.map(r => ({ date: `2025-09-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }


// F: date printed once per day, two-line column headings ("Money" / "out"), dollar signs
{ const rows = make(30, 4100, 6); let prevDay = 0;
  const body = rows.map(r => { const d = r.day !== prevDay ? p2(r.day) + " Aug 2025" : ""; prevDay = r.day;
    return `<tr><td>${d}</td><td>${r.desc}</td><td class=r>${r.debit ? "$" + fmt(r.debit) : ""}</td><td class=r>${r.credit ? "$" + fmt(r.credit) : ""}</td><td class=r>${fmt(r.bal)}</td></tr>`; }).join("");
  files.daily_f = { html: `${css}<h1>Complete Access</h1><p class=meta>Statement period 1 Aug 2025 - 31 Aug 2025</p>
  <table><thead><tr><th>Date</th><th>Transaction</th><th class=r>Money<br>out</th><th class=r>Money<br>in</th><th class=r>Balance</th></tr></thead><tbody>${body}</tbody></table>`,
    expected: rows.map(r => ({ date: `2025-08-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }

// G: two date columns (transaction and posted date) and a single signed amount
{ const rows = make(22, 0, 7); rows.forEach((r, i) => { if (r.credit || i % 9 === 8) { r.debit = 0; r.credit = 250; r.desc = "PAYMENT - THANK YOU"; } });
  const body = rows.map(r => `<tr><td>${p2(r.day)}/07/2025</td><td>${p2(Math.min(28, r.day + 1))}/07/2025</td><td>${r.desc}</td><td class=r>${r.credit ? "-" + fmt(r.credit) : fmt(r.debit)}</td></tr>`).join("");
  files.twodate_g = { html: `${css}<h1>Platinum Card</h1><p class=meta>Statement period 01/07/2025 to 31/07/2025</p>
  <table><thead><tr><th>Transaction date</th><th>Posted date</th><th>Description</th><th class=r>Amount</th></tr></thead><tbody>${body}</tbody></table>`,
    expected: rows.map(r => ({ date: `2025-07-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }

// H: monospace "text style" statement: columns are made of spaces inside one string per line
{ const rows = make(26, 1800, 8); const pad = (t, n) => (t + " ".repeat(n)).slice(0, n), padl = (t, n) => (" ".repeat(n) + t).slice(-n);
  const lines = rows.map(r => `${p2(r.day)} Jun 2025  ${pad(r.desc, 30)}${padl(r.debit ? fmt(r.debit) : "", 11)}${padl(r.credit ? fmt(r.credit) : "", 11)}${padl(fmt(r.bal), 12)}`);
  files.mono_h = { html: `<style>@page{size:A4;margin:14mm}pre{font:9pt "Courier New",monospace;margin:0}</style><pre>MONTHLY STATEMENT           Period: 01 Jun 2025 to 30 Jun 2025

DATE         DESCRIPTION                        DEBIT     CREDIT     BALANCE
${lines.join(String.fromCharCode(10))}

Closing balance ${fmt(rows[rows.length - 1].bal)}</pre>`,
    expected: rows.map(r => ({ date: `2025-06-${p2(r.day)}`, desc: r.desc, debit: r.debit || 0, credit: r.credit || 0 })) }; }


// I: a scanned statement (an image, no text layer) must be rejected with a helpful message
files.scan_i = { html: `<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==" style="width:600px;height:800px">`, expected: [] };

for (const [name, f] of Object.entries(files)) {
  const html = path.join(out, name + ".html");
  fs.writeFileSync(html, `<!doctype html><meta charset=utf-8>${f.html}`);
  fs.writeFileSync(path.join(out, name + ".expected.json"), JSON.stringify(f.expected, null, 1));
  cp.execFileSync(browser, ["--headless=new", "--disable-gpu", "--no-pdf-header-footer", `--print-to-pdf=${path.join(out, name + ".pdf")}`, "file:///" + html.split(path.sep).join("/")], { stdio: "ignore" });
  fs.unlinkSync(html);
  console.log(name, fs.statSync(path.join(out, name + ".pdf")).size + " bytes", f.expected.length + " transactions");
}
