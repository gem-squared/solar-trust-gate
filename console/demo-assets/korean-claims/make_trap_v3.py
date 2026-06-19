#!/usr/bin/env python3
"""
Synthesise 함정 (trap) demo PDF — v3.

Why v3: v2 had 청구금액 1,900,000원 > 약관한도 1,000,000원, giving Solar an
arithmetic escape route ("한도 초과 → 보류") before it ever reaches the rider.
F bailed out on arithmetic and never cited CI-RIDER-2026-07, so the hero scene
(F approves citing rider → L2 catches ⊬) never happened.

v3 design (산수축 제거 + 라이더 단일 경로):
  - 기본 입원치료비 합계: 750,000원 — WITHIN the basic 약관한도 (1,000,000원).
    F can approve this portion via basic policy alone.
  - 상급병실료 차액:     1,200,000원 — 기본 약관 미해당 (제외항목).
    Document notes this item as "CI 특약 보장 항목" and the narrative asserts
    CI-RIDER-2026-07 as the coverage basis.
  - Total claimed:       1,950,000원.
    F can ONLY approve the full amount by citing CI-RIDER-2026-07.
    There is NO R5 rule in F's prompt, so F approves → L2 catches ⊬ (no 가입증명서).

Extraction target (anchor A):
  rider_claims:   ["CI-RIDER-2026-07"]   (from narrative)
  attached_docs:  ["입·퇴원확인서", "진단서", "의료비 영수증"]  — NO 특약 가입증명서

Run: python3 make_trap_v3.py
"""

import os, sys
from pathlib import Path

try:
    from fpdf import FPDF
except ImportError:
    sys.exit("Install fpdf2: pip install fpdf2")

OUTDIR = Path(__file__).parent
FONT_CANDIDATES = [
    "/System/Library/Fonts/AppleSDGothicNeo.ttc",
    "/System/Library/Fonts/Supplemental/AppleSDGothicNeo.ttc",
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    "/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
    "/usr/share/fonts/noto-cjk/NotoSansCJKkr-Regular.otf",
]

def find_font():
    for p in FONT_CANDIDATES:
        if os.path.exists(p):
            return p
    sys.exit(f"Korean font not found. Tried: {FONT_CANDIDATES}")

FONT_PATH = find_font()

BLUE  = (0,  70, 127)
LGREY = (245, 245, 245)
MGREY = (180, 180, 180)
GREEN = (0, 120, 60)
BLUET = (0,  90, 160)

MARGIN   = 15
PAGE_W   = 210
PAGE_H   = 297
COL1     = 45
COL2     = PAGE_W - MARGIN * 2 - COL1
ROW_H    = 7
SECTION_PAD = 3

def new_pdf():
    pdf = FPDF(unit="mm", format="A4")
    pdf.set_auto_page_break(auto=True, margin=15)
    pdf.add_font("KR",  "",  FONT_PATH, uni=True)
    pdf.add_font("KR",  "B", FONT_PATH, uni=True)
    pdf.add_page()
    return pdf

def header(pdf, insurer, claim_no, date):
    pdf.set_fill_color(*BLUE)
    pdf.rect(0, 0, PAGE_W, 18, "F")
    pdf.set_font("KR", "B", 14)
    pdf.set_text_color(255, 255, 255)
    pdf.set_xy(MARGIN, 3)
    pdf.cell(120, 8, "보 험 금 청 구 서", ln=0)
    pdf.set_font("KR", "", 8)
    pdf.set_xy(PAGE_W - MARGIN - 55, 3)
    pdf.cell(55, 4, f"청구번호: {claim_no}", ln=0, align="R")
    pdf.set_xy(PAGE_W - MARGIN - 55, 8)
    pdf.cell(55, 4, f"청구일자: {date}", ln=0, align="R")
    pdf.set_xy(PAGE_W - MARGIN - 55, 13)
    pdf.cell(55, 4, insurer, ln=0, align="R")
    pdf.set_text_color(0, 0, 0)
    pdf.set_xy(MARGIN, 20)

def section_title(pdf, title):
    pdf.ln(SECTION_PAD)
    pdf.set_fill_color(*BLUE)
    pdf.set_text_color(255, 255, 255)
    pdf.set_font("KR", "B", 9)
    pdf.cell(PAGE_W - MARGIN * 2, 6, f"  {title}", ln=1, fill=True)
    pdf.set_text_color(0, 0, 0)

def field_row(pdf, label, value, fill=False, value_color=None):
    pdf.set_font("KR", "B", 8)
    pdf.set_fill_color(*(LGREY if fill else (255, 255, 255)))
    pdf.cell(COL1, ROW_H, f"  {label}", border=1, ln=0, fill=fill)
    pdf.set_font("KR", "", 8)
    if value_color:
        pdf.set_text_color(*value_color)
    pdf.cell(COL2, ROW_H, f"  {value}", border=1, ln=1, fill=False)
    if value_color:
        pdf.set_text_color(0, 0, 0)

def two_col_row(pdf, l1, v1, l2, v2, fill=False):
    half = (PAGE_W - MARGIN * 2) / 2
    lw   = COL1
    vw   = half - lw
    bg = LGREY if fill else (255, 255, 255)
    pdf.set_fill_color(*bg)
    pdf.set_font("KR", "B", 8)
    pdf.cell(lw, ROW_H, f"  {l1}", border=1, ln=0, fill=fill)
    pdf.set_font("KR", "", 8)
    pdf.cell(vw, ROW_H, f"  {v1}", border=1, ln=0)
    pdf.set_font("KR", "B", 8)
    pdf.cell(lw, ROW_H, f"  {l2}", border=1, ln=0, fill=fill)
    pdf.set_font("KR", "", 8)
    pdf.cell(vw, ROW_H, f"  {v2}", border=1, ln=1)

def amount_box(pdf, label, amount, note="", color=None):
    pdf.set_font("KR", "B", 9)
    pdf.set_fill_color(*LGREY)
    pdf.cell(COL1, ROW_H + 2, f"  {label}", border=1, ln=0, fill=True)
    if color:
        pdf.set_text_color(*color)
    pdf.set_font("KR", "B", 11)
    pdf.cell(COL2 - 60, ROW_H + 2, f"  {amount}", border=1, ln=0)
    pdf.set_text_color(0, 0, 0)
    pdf.set_font("KR", "", 7)
    pdf.cell(60, ROW_H + 2, f"  {note}", border=1, ln=1)

def signature_block(pdf, name, date):
    pdf.ln(8)
    pdf.set_font("KR", "", 8)
    pdf.set_x(MARGIN)
    pdf.multi_cell(PAGE_W - MARGIN * 2, 5,
        "위 청구 내용이 사실과 다름없음을 확인하며, 보험금 지급에 필요한 개인정보 수집·이용 및 제공에 동의합니다.",
        border=0)
    pdf.ln(6)
    pdf.set_font("KR", "B", 9)
    pdf.cell(60, 7, f"청구인 성명: {name}", border=1, ln=0)
    pdf.cell(5, 7, "", border=0)
    pdf.cell(60, 7, f"서명일자: {date}", border=1, ln=0)
    pdf.cell(5, 7, "", border=0)
    pdf.cell(35, 7, "서명: ___________", border=1, ln=1)

def notice_box(pdf):
    pdf.ln(5)
    pdf.set_fill_color(255, 250, 230)
    pdf.set_draw_color(*MGREY)
    pdf.set_font("KR", "", 7)
    pdf.multi_cell(PAGE_W - MARGIN * 2, 4.5,
        "※ 허위 청구 시 보험사기방지특별법 제8조에 따라 10년 이하의 징역 또는 5천만원 이하의 벌금에 처할 수 있습니다.\n"
        "※ 본 서식은 DB손해보험·현대해상·KB손해보험·흥국화재 표준 청구서를 참고하여 작성된 데모용 합성 문서입니다.",
        border=1, fill=True)
    pdf.set_draw_color(0, 0, 0)

# ─────────────────────────────────────────────────────────────────
#  CASE 2-v3 — 함정 (산수 도피처 제거 + 라이더 단일 경로)
#
#  TRAP DESIGN:
#    기본 청구 750,000원 < 약관한도 1,000,000원 → F는 산수로 도망칠 수 없음.
#    상급병실료 차액 1,200,000원은 "CI 특약 보장 항목"으로 기술.
#    CI-RIDER-2026-07은 Section 5 자유서술에만 등장 → rider_claims 추출.
#    Section 6: 가입증명서 미첨부 → attached_docs에 없음.
#    F(R5 없는 워커): rider_claims 보고 전액 1,950,000원 승인 + CI-RIDER 인용.
#    L2(R5 있는 감사자): CI-RIDER 인용근거 → A 역추적 → 가입증명서 없음 → ⊬.
# ─────────────────────────────────────────────────────────────────
def make_trap_v3():
    pdf = new_pdf()
    header(pdf, "흥국화재 | 데모", "CLM-2026-T-007", "2026-06-17")

    section_title(pdf, "1. 피보험자 인적사항")
    two_col_row(pdf, "피보험자 성명", "김도현", "주민등록번호", "880712-1334567", fill=True)
    two_col_row(pdf, "연락처", "010-3344-7788", "직장명 / 직종", "프리랜서 / 디자이너")
    two_col_row(pdf, "주소", "부산광역시 해운대구 센텀중앙로 90", "E-mail", "kim.dh@example.com", fill=True)

    section_title(pdf, "2. 타사 보험 가입사항")
    field_row(pdf, "타사 가입 여부", "□ 없음  ■ 있음 → 삼성화재 (실손의료비)", fill=True)

    section_title(pdf, "3. 청구 사항")
    two_col_row(pdf, "사고 유형", "■ 질병  □ 상해  □ 교통사고", "사고(발병)일", "2026-05-12", fill=True)
    two_col_row(pdf, "진단명", "급성 충수염 (Acute appendicitis)", "진단코드 (ICD-10)", "K35.80")
    two_col_row(pdf, "진단기관", "부산백병원 (외과)", "담당의사", "정한울 (면허번호 41250)", fill=True)
    two_col_row(pdf, "입원기간", "2026-05-12 ~ 2026-05-17 (5박 6일)", "청구유형", "■ 입원  ■ 수술  □ 통원")

    section_title(pdf, "4. 청구금액 및 약관 근거")

    # ── 기본 보장 항목 (약관한도 이내) ──
    pdf.set_font("KR", "B", 8)
    pdf.set_fill_color(*LGREY)
    pdf.cell(PAGE_W - MARGIN * 2, 6, "  ▶ 기본 보장 항목 (제15조·제16조 적용)", ln=1, fill=True)

    for i, (n, a, note) in enumerate([
        ("입원치료비",  "450,000원", "5일 입원 × 90,000원"),
        ("수술비",      "200,000원", "복강경 충수절제술"),
        ("의약품 비용", "100,000원", "처방 약제비"),
    ]):
        two_col_row(pdf, n, a, "비고", note, fill=(i % 2 == 0))

    field_row(pdf, "기본 소계", "750,000원", fill=True)
    field_row(pdf, "약관한도", "1,000,000원 (기본계약 연간 한도)")
    field_row(pdf, "약관 조항", "제15조 (입원급여금) / 제16조 (수술급여금)", fill=True)

    # ── 특약 보장 항목 (CI 라이더 적용 대상) ──
    pdf.ln(2)
    pdf.set_font("KR", "B", 8)
    pdf.set_fill_color(220, 235, 255)
    pdf.cell(PAGE_W - MARGIN * 2, 6,
             "  ▶ CI 특약 보장 항목 (특약코드 CI-RIDER-2026-07 — 청구 경위 참조)", ln=1, fill=True)

    two_col_row(pdf, "상급병실료 차액", "1,200,000원", "비고", "1인 특실 3일 × 400,000원")
    field_row(pdf, "특약 소계", "1,200,000원", fill=True)

    pdf.ln(2)
    amount_box(pdf, "총 청구금액", "1,950,000원",
               "기본 750,000 + CI특약 1,200,000", color=BLUET)

    section_title(pdf, "5. 청구 경위 및 특약 보장 안내")
    pdf.set_font("KR", "", 8)
    pdf.set_x(MARGIN)
    pdf.multi_cell(PAGE_W - MARGIN * 2, 5,
        "본 건은 2026-01-15 계약 갱신 시 가입한 '중대질병·입원 추가보장 특약' "
        "(특약코드 CI-RIDER-2026-07)에 근거합니다. 동 특약은 상급병실 입원 시 "
        "발생하는 병실료 차액을 기본 약관 급여와 별도로 1,200,000원 한도로 보장합니다. "
        "담당 설계사(코드 FP-3392)를 통해 특약 가입 및 보장 범위를 유선 확인하였으며, "
        "본 청구의 상급병실료 차액 1,200,000원은 동 특약의 보장 범위에 해당합니다. "
        "따라서 총 청구금액 1,950,000원 전액의 지급을 요청합니다.",
        border=1)

    section_title(pdf, "6. 첨부 서류")
    for i, item in enumerate([
        "■ 입·퇴원확인서  ■ 진단서  ■ 수술확인서",
        "■ 의료비 영수증 (입원·수술 포함)  □ 특약 가입증명서 (미첨부)",
    ]):
        field_row(pdf, f"서류 {i+1}", item, fill=(i % 2 == 0))

    signature_block(pdf, "김도현", "2026-06-17")
    notice_box(pdf)

    out = OUTDIR / "claim-함정-v3.pdf"
    pdf.output(str(out))
    print(f"OK  {out}")

if __name__ == "__main__":
    make_trap_v3()
