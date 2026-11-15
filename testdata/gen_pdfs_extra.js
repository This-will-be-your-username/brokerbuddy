// Extra synthetic fixtures (all data made up): a statement with a legal footer printed on every page,
// as many banks do. Run: node testdata/gen_pdfs_extra.js   (needs Edge or Chrome)
const fs = require("fs"), path = require("path"), cp = require("child_process");
const out = path.join(__dirname, "pdf");
const browsers = ["C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe", "C:/Program Files/Google/Chrome/Application/chrome.exe"];
const browser = browsers.find(fs.existsSync);
if (!browser) throw new Error("no Edge/Chrome found");

const fmt = n => n.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const p2 = n => String(n).padStart(2, "0");
const merchants = ["WOOLWORTHS 1234 SYDNEY AUS", "STARBUCKS 22 NEWTOWN AUS", "AGL ENERGY", "BUNNINGS 7788 PENRITH AUS", "NETFLIX.COM", "CHEMIST WAREHOUSE 14 SYDNEY AUS", "ALDI STORES 88 AUS", "BP 5566 PARRAMATTA AUS"];

// F1: 60 purchases (two pages), card-style layout with channel wording, and a footer on each page
const rows = [];
let bal = 5000;
for (let i = 0; i < 60; i++) {
  const d = Math.round((10 + (i * 7.3) % 90) * 100) / 100;
  bal = Math.round((bal - d) * 100) / 100;
  rows.push({ day: 1 + (i % 28), desc: merchants[i % merchants.length], debit: d, bal });
}
const body = rows.map(r => `<tr><td>${p2(r.day)}/05/2026</td><td>${r.desc} - Visa Purchase - Receipt ${100000 + r.day * 37 + Math.round(r.debit)}</td><td class=r>${fmt(r.debit)}</td><td class=r></td><td class=r>${fmt(r.bal)}</td></tr>`).join("");
const html = `<!doctype html><meta charset=utf-8><style>@page{size:A4;margin:14mm 14mm 22mm}body{font:9pt Arial,sans-serif}h1{font-size:14pt;margin:0 0 4px}
table{width:100%;border-collapse:collapse}thead{display:table-header-group}th{text-align:left;border-bottom:1px solid #333;padding:3px 4px}td{padding:3px 4px;vertical-align:top}.r{text-align:right;width:62px;padding-left:26px}
.foot{position:fixed;bottom:0;left:0;right:0;font-size:6.5pt;color:#555;border-top:1px solid #aaa;padding-top:3px}</style>
<h1>Choice Account</h1><p>Statement period 1 May 2026 - 31 May 2026</p>
<table><thead><tr><th>Date</th><th>Transaction details</th><th class=r>Debit</th><th class=r>Credit</th><th class=r>Balance</th></tr></thead><tbody>${body}</tbody></table>
<div class=foot>Copyright © 2026 Example Banking Corporation ABN 12 345 678 901 AFSL 123456. Terms and conditions apply. www.example.com.au</div>`;
fs.writeFileSync(path.join(out, "footer_j.html"), html);
fs.writeFileSync(path.join(out, "footer_j.expected.json"), JSON.stringify(rows.map(r => ({ date: `2026-05-${p2(r.day)}`, desc: r.desc, debit: r.debit, credit: 0 })), null, 1));
cp.execFileSync(browser, ["--headless=new", "--disable-gpu", "--no-pdf-header-footer", `--print-to-pdf=${path.join(out, "footer_j.pdf")}`, "file:///" + path.join(out, "footer_j.html").split(path.sep).join("/")], { stdio: "ignore" });
fs.unlinkSync(path.join(out, "footer_j.html"));
console.log("footer_j", fs.statSync(path.join(out, "footer_j.pdf")).size + " bytes", rows.length + " transactions");
