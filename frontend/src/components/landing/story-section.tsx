"use client";

import { useEffect, useRef, useCallback } from "react";
import { pseudoRandom } from "./canvas-utils";

interface StorySectionProps {
  isDark: boolean;
}

export default function StorySection({ isDark }: StorySectionProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  const draw = useCallback(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const rawCtx = canvas.getContext("2d");
    if (!rawCtx) return;
    const ctx: CanvasRenderingContext2D = rawCtx;

    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    const w = rect.width;
    const h = rect.height;
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);

    const FONT = '"Courier New", Courier, monospace';

    // ── Document sizing ──
    const docW = Math.min(w * 0.9, 480);
    const fontSize = Math.max(9.5, Math.min(13, docW * 0.027));
    const lineHeight = fontSize * 2;
    const margin = Math.max(docW * 0.07, 22);
    const textAreaW = docW - margin * 2;

    // Word-wrap body text
    const bodyText =
      "In 1943, a team of Lockheed engineers built America\u2019s first jet fighter in just 143 days. Proof that a small team with the right tools can do the impossible.";

    ctx.font = `${fontSize}px ${FONT}`;
    const words = bodyText.split(" ");
    const bodyLines: string[] = [];
    let curLine = "";
    for (const word of words) {
      const test = curLine ? curLine + " " + word : word;
      if (ctx.measureText(test).width > textAreaW && curLine) {
        bodyLines.push(curLine);
        curLine = word;
      } else {
        curLine = test;
      }
    }
    if (curLine) bodyLines.push(curLine);

    // Calculate document height from content
    const contentH =
      margin * 0.65 +
      lineHeight * 0.55 +
      lineHeight * 0.45 +
      lineHeight * 1.05 +
      lineHeight * 1.35 +
      bodyLines.length * lineHeight +
      margin * 0.6;
    const docH = Math.max(contentH, docW * 0.42);
    const docX = (w - docW) / 2;
    const docY = (h - docH) / 2;
    const centerX = docX + docW / 2;
    const centerY = docY + docH / 2;

    ctx.save();
    ctx.translate(centerX, centerY);
    ctx.rotate(-0.005);
    ctx.translate(-centerX, -centerY);

    // ── Drop shadow ──
    ctx.save();
    ctx.shadowColor = isDark ? "rgba(0,0,0,0.65)" : "rgba(0,0,0,0.16)";
    ctx.shadowBlur = isDark ? 35 : 22;
    ctx.shadowOffsetX = 2;
    ctx.shadowOffsetY = 4;
    ctx.fillStyle = "#eee8d5";
    ctx.fillRect(docX, docY, docW, docH);
    ctx.restore();

    // ── Paper base ──
    const paperGrad = ctx.createLinearGradient(
      docX,
      docY,
      docX + docW * 0.3,
      docY + docH
    );
    paperGrad.addColorStop(0, "#f4efe3");
    paperGrad.addColorStop(0.5, "#f0ebd8");
    paperGrad.addColorStop(1, "#ebe5cf");
    ctx.fillStyle = paperGrad;
    ctx.fillRect(docX, docY, docW, docH);

    // ── Paper grain (scattered noise dots) ──
    for (let i = 0; i < 1200; i++) {
      const nx = docX + pseudoRandom(i * 3) * docW;
      const ny = docY + pseudoRandom(i * 3 + 1) * docH;
      const val = pseudoRandom(i * 3 + 2);
      ctx.fillStyle =
        val > 0.5
          ? `rgba(255,255,248,${0.02 + val * 0.04})`
          : `rgba(90,70,30,${0.01 + val * 0.02})`;
      ctx.fillRect(nx, ny, 1, 1);
    }

    // ── Edge aging (darkened borders) ──
    // Top
    const topGrad = ctx.createLinearGradient(
      docX, docY, docX, docY + docH * 0.1
    );
    topGrad.addColorStop(0, "rgba(90,70,28,0.05)");
    topGrad.addColorStop(1, "rgba(90,70,28,0)");
    ctx.fillStyle = topGrad;
    ctx.fillRect(docX, docY, docW, docH * 0.1);
    // Bottom
    const botGrad = ctx.createLinearGradient(
      docX, docY + docH * 0.9, docX, docY + docH
    );
    botGrad.addColorStop(0, "rgba(90,70,28,0)");
    botGrad.addColorStop(1, "rgba(90,70,28,0.06)");
    ctx.fillStyle = botGrad;
    ctx.fillRect(docX, docY + docH * 0.9, docW, docH * 0.1);
    // Left
    const leftGrad = ctx.createLinearGradient(
      docX, docY, docX + docW * 0.07, docY
    );
    leftGrad.addColorStop(0, "rgba(90,70,28,0.04)");
    leftGrad.addColorStop(1, "rgba(90,70,28,0)");
    ctx.fillStyle = leftGrad;
    ctx.fillRect(docX, docY, docW * 0.07, docH);
    // Right
    const rightGrad = ctx.createLinearGradient(
      docX + docW * 0.93, docY, docX + docW, docY
    );
    rightGrad.addColorStop(0, "rgba(90,70,28,0)");
    rightGrad.addColorStop(1, "rgba(90,70,28,0.04)");
    ctx.fillStyle = rightGrad;
    ctx.fillRect(docX + docW * 0.93, docY, docW * 0.07, docH);

    // ── Fox spots (tiny age marks) ──
    for (let i = 0; i < 8; i++) {
      ctx.fillStyle = `rgba(135,100,40,${0.012 + pseudoRandom(i * 19 + 5) * 0.02})`;
      ctx.beginPath();
      ctx.arc(
        docX + pseudoRandom(i * 19 + 7) * docW,
        docY + pseudoRandom(i * 19 + 11) * docH,
        0.4 + pseudoRandom(i * 19 + 3) * 1.6,
        0,
        Math.PI * 2
      );
      ctx.fill();
    }

    // ── Fold crease (horizontal) ──
    const foldY = docY + docH * 0.52;
    ctx.strokeStyle = "rgba(85,65,30,0.045)";
    ctx.lineWidth = 0.5;
    ctx.beginPath();
    ctx.moveTo(docX + 1, foldY);
    ctx.lineTo(docX + docW - 1, foldY);
    ctx.stroke();
    // Light side (paper catching light at fold)
    ctx.strokeStyle = "rgba(255,255,238,0.05)";
    ctx.lineWidth = 0.4;
    ctx.beginPath();
    ctx.moveTo(docX + 1, foldY - 0.7);
    ctx.lineTo(docX + docW - 1, foldY - 0.7);
    ctx.stroke();

    // ── Typewriter character renderer ──
    const textL = docX + margin;
    const textR = docX + docW - margin;

    function typeChar(
      ch: string,
      x: number,
      y: number,
      sz: number,
      bold: boolean,
      seed: number,
      emphasis = false
    ) {
      const jx = (pseudoRandom(seed) - 0.5) * 0.45;
      const jy = (pseudoRandom(seed + 1) - 0.5) * 0.3;
      const aBase = emphasis ? 0.88 : 0.6;
      const aRange = emphasis ? 0.12 : 0.4;
      const a = aBase + pseudoRandom(seed + 2) * aRange;

      ctx.font = `${bold ? "bold " : ""}${sz}px ${FONT}`;
      ctx.textBaseline = "top";
      ctx.textAlign = "left";
      ctx.fillStyle = `rgba(10,8,4,${a})`;
      ctx.fillText(ch, x + jx, y + jy);

      // Ink bleed on heavy strikes
      if (a > 0.87 && ch.trim()) {
        ctx.fillStyle = `rgba(10,8,4,${(a - 0.87) * 0.22})`;
        ctx.fillText(ch, x + jx + 0.2, y + jy + 0.2);
      }
      return ctx.measureText(ch).width;
    }

    function typeLine(
      text: string,
      x: number,
      y: number,
      sz: number,
      bold = false,
      emphStr?: string
    ) {
      let cx = x;
      const ei = emphStr ? text.indexOf(emphStr) : -1;
      const eEnd = ei >= 0 ? ei + emphStr!.length : -1;
      for (let i = 0; i < text.length; i++) {
        const seed = Math.floor(x * 131 + y * 317 + i * 71);
        const emph = ei >= 0 && i >= ei && i < eEnd;
        cx += typeChar(text[i], cx, y, sz, bold, seed, emph);
      }
    }

    // ── Render document text ──
    let cy = docY + margin * 0.65;

    // Classification: SECRET (struck through)
    ctx.font = `${fontSize * 0.7}px ${FONT}`;
    ctx.textAlign = "center";
    ctx.textBaseline = "top";
    ctx.fillStyle = "rgba(10,8,4,0.2)";
    ctx.fillText("SECRET", centerX, cy);
    const secW = ctx.measureText("SECRET").width;
    ctx.strokeStyle = "rgba(10,8,4,0.25)";
    ctx.lineWidth = 0.7;
    ctx.beginPath();
    ctx.moveTo(centerX - secW / 2 - 3, cy + fontSize * 0.3);
    ctx.lineTo(centerX + secW / 2 + 3, cy + fontSize * 0.3);
    ctx.stroke();
    cy += lineHeight * 0.55;

    // Horizontal rule
    ctx.strokeStyle = "rgba(10,8,4,0.08)";
    ctx.lineWidth = 0.35;
    ctx.beginPath();
    ctx.moveTo(textL, cy);
    ctx.lineTo(textR, cy);
    ctx.stroke();
    cy += lineHeight * 0.45;

    // MEMORANDUM
    typeLine("MEMORANDUM", textL, cy, fontSize * 1.15, true);
    cy += lineHeight * 1.05;

    // SUBJECT
    typeLine("SUBJECT: XP-80 Shooting Star", textL, cy, fontSize);
    cy += lineHeight * 1.35;

    // Body text (with "143" emphasized)
    for (const line of bodyLines) {
      typeLine(line, textL, cy, fontSize, false, "143");
      cy += lineHeight;
    }

    // ── DECLASSIFIED rubber stamp (rendered on offscreen canvas) ──
    const stampFontSize = Math.max(12, docW * 0.038);
    ctx.font = `bold ${stampFontSize}px ${FONT}`;
    const stampLabel = "DECLASSIFIED";
    const stampTextW = ctx.measureText(stampLabel).width;
    const stampW = stampTextW + stampFontSize * 0.85;
    const stampH = stampFontSize * 1.65;
    const stampPad = 10;

    const offscreen = document.createElement("canvas");
    offscreen.width = (stampW + stampPad * 2) * dpr;
    offscreen.height = (stampH + stampPad * 2) * dpr;
    const oc = offscreen.getContext("2d")!;
    oc.setTransform(dpr, 0, 0, dpr, 0, 0);
    const ocx = (stampW + stampPad * 2) / 2;
    const ocy = (stampH + stampPad * 2) / 2;

    // Double border
    oc.strokeStyle = "rgba(155,22,12,0.52)";
    oc.lineWidth = 2;
    oc.strokeRect(ocx - stampW / 2, ocy - stampH / 2, stampW, stampH);
    oc.lineWidth = 0.7;
    oc.strokeRect(
      ocx - stampW / 2 + 3,
      ocy - stampH / 2 + 3,
      stampW - 6,
      stampH - 6
    );

    // Text
    oc.font = `bold ${stampFontSize}px ${FONT}`;
    oc.textAlign = "center";
    oc.textBaseline = "middle";
    oc.fillStyle = "rgba(155,22,12,0.52)";
    oc.fillText(stampLabel, ocx, ocy);

    // Rubber wear effect (punch holes in stamp ink)
    oc.globalCompositeOperation = "destination-out";
    for (let i = 0; i < 90; i++) {
      oc.fillStyle = `rgba(0,0,0,${0.1 + pseudoRandom(i * 11 + 4) * 0.45})`;
      oc.beginPath();
      oc.arc(
        pseudoRandom(i * 11 + 1) * (stampW + stampPad * 2),
        pseudoRandom(i * 11 + 2) * (stampH + stampPad * 2),
        0.25 + pseudoRandom(i * 11 + 3) * 1.8,
        0,
        Math.PI * 2
      );
      oc.fill();
    }
    // Horizontal streaks (rolling pressure artifact)
    for (let i = 0; i < 5; i++) {
      oc.fillStyle = `rgba(0,0,0,${0.06 + pseudoRandom(i * 29 + 201) * 0.12})`;
      oc.fillRect(
        0,
        pseudoRandom(i * 29 + 200) * (stampH + stampPad * 2),
        stampW + stampPad * 2,
        0.3 + pseudoRandom(i * 29 + 202) * 0.8
      );
    }

    // Composite stamp onto document
    ctx.save();
    ctx.translate(docX + docW * 0.62, docY + docH * 0.16);
    ctx.rotate(-0.1);
    ctx.drawImage(
      offscreen,
      -(stampW + stampPad * 2) / 2,
      -(stampH + stampPad * 2) / 2,
      stampW + stampPad * 2,
      stampH + stampPad * 2
    );
    ctx.restore();

    // ── Coffee ring stain (very subtle) ──
    const ringX = docX + docW * 0.83;
    const ringY = docY + docH * 0.74;
    const ringR = docW * 0.045;
    ctx.strokeStyle = "rgba(120,85,35,0.025)";
    ctx.lineWidth = ringR * 0.15;
    ctx.beginPath();
    ctx.arc(ringX, ringY, ringR, 0.3, Math.PI * 1.75);
    ctx.stroke();

    // ── Paper edge border ──
    ctx.strokeStyle = "rgba(100,80,45,0.1)";
    ctx.lineWidth = 0.4;
    ctx.strokeRect(docX, docY, docW, docH);

    ctx.restore();
  }, [isDark]);

  useEffect(() => {
    draw();
    window.addEventListener("resize", draw);
    return () => window.removeEventListener("resize", draw);
  }, [draw]);

  return (
    <section
      className="relative py-16 sm:py-20 px-6 sm:px-10 overflow-hidden"
      style={{ background: isDark ? "#0c0c14" : "#f5f7fa" }}
    >
      <div className="mx-auto max-w-5xl flex flex-col items-center">
        <div
          className="w-full max-w-[560px]"
          style={{ aspectRatio: "560 / 380" }}
        >
          <canvas
            ref={canvasRef}
            style={{ width: "100%", height: "100%", display: "block" }}
          />
        </div>
      </div>
    </section>
  );
}
