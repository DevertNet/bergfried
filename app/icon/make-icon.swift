// Draws the Bergfried app icon (tower in front of mountains at dawn) and writes an .iconset.
// Aufruf: swiftc -o /tmp/mkicon icon/make-icon.swift && /tmp/mkicon icon/AppIcon.iconset && iconutil -c icns icon/AppIcon.iconset
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

func rgb(_ r: CGFloat, _ g: CGFloat, _ b: CGFloat, _ a: CGFloat = 1) -> CGColor { CGColor(red: r, green: g, blue: b, alpha: a) }

func draw(size s: Int) -> CGImage {
    let cs = CGColorSpace(name: CGColorSpace.sRGB)!
    let ctx = CGContext(data: nil, width: s, height: s, bitsPerComponent: 8, bytesPerRow: 0, space: cs,
                        bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    let S = CGFloat(s)
    ctx.scaleBy(x: S, y: S)           // Einheitsquadrat, Ursprung unten links
    ctx.interpolationQuality = .high

    func grad(_ colors: [CGColor], _ from: CGPoint, _ to: CGPoint) {
        let g = CGGradient(colorsSpace: cs, colors: colors as CFArray, locations: nil)!
        ctx.drawLinearGradient(g, start: from, end: to, options: [.drawsBeforeStartLocation, .drawsAfterEndLocation])
    }
    func poly(_ pts: [(CGFloat, CGFloat)], _ color: CGColor) {
        ctx.setFillColor(color)
        ctx.beginPath(); ctx.move(to: CGPoint(x: pts[0].0, y: pts[0].1))
        for p in pts.dropFirst() { ctx.addLine(to: CGPoint(x: p.0, y: p.1)) }
        ctx.closePath(); ctx.fillPath()
    }

    // Form: abgerundetes Quadrat (macOS-Stil)
    let r: CGFloat = 0.225
    let shape = CGPath(roundedRect: CGRect(x: 0.04, y: 0.04, width: 0.92, height: 0.92), cornerWidth: r * 0.92, cornerHeight: r * 0.92, transform: nil)
    ctx.saveGState()
    // weicher Schatten
    ctx.setShadow(offset: CGSize(width: 0, height: -0.012), blur: 0.03, color: rgb(0, 0, 0, 0.35))
    ctx.addPath(shape); ctx.setFillColor(rgb(0.1, 0.1, 0.2)); ctx.fillPath()
    ctx.restoreGState()
    ctx.saveGState()
    ctx.addPath(shape); ctx.clip()

    // Himmel: Nachtblau oben, Violett, Morgenrot am Horizont
    grad([rgb(0.07, 0.10, 0.26), rgb(0.27, 0.20, 0.50), rgb(0.93, 0.50, 0.40), rgb(1.0, 0.78, 0.45)],
         CGPoint(x: 0.5, y: 0.96), CGPoint(x: 0.5, y: 0.30))
    // Sonne
    ctx.setFillColor(rgb(1.0, 0.92, 0.65, 0.95))
    ctx.fillEllipse(in: CGRect(x: 0.20, y: 0.40, width: 0.17, height: 0.17))
    ctx.setFillColor(rgb(1.0, 0.85, 0.55, 0.25))
    ctx.fillEllipse(in: CGRect(x: 0.145, y: 0.345, width: 0.28, height: 0.28))
    // Sterne
    ctx.setFillColor(rgb(1, 1, 1, 0.85))
    for (x, y, d) in [(0.18, 0.84, 0.014), (0.34, 0.90, 0.010), (0.72, 0.88, 0.013), (0.84, 0.76, 0.009), (0.60, 0.92, 0.008)] as [(CGFloat, CGFloat, CGFloat)] {
        ctx.fillEllipse(in: CGRect(x: x, y: y, width: d, height: d))
    }

    // Distant and near mountains
    poly([(0, 0.30), (0.12, 0.46), (0.22, 0.38), (0.38, 0.58), (0.52, 0.42), (0.66, 0.54), (0.80, 0.40), (0.92, 0.50), (1, 0.42), (1, 0), (0, 0)], rgb(0.30, 0.25, 0.52))
    poly([(0, 0.22), (0.14, 0.34), (0.28, 0.24), (0.46, 0.36), (0.62, 0.26), (0.78, 0.38), (0.90, 0.28), (1, 0.34), (1, 0), (0, 0)], rgb(0.14, 0.15, 0.32))

    // Turm (Bergfried)
    let bx: CGFloat = 0.385, bw: CGFloat = 0.23, top: CGFloat = 0.66
    // Body
    ctx.saveGState()
    ctx.addRect(CGRect(x: bx, y: 0.12, width: bw, height: top - 0.12)); ctx.clip()
    grad([rgb(0.95, 0.88, 0.76), rgb(0.70, 0.64, 0.60)], CGPoint(x: bx, y: 0.4), CGPoint(x: bx + bw, y: 0.4))
    // Schattenseite
    ctx.setFillColor(rgb(0.25, 0.20, 0.35, 0.28))
    ctx.fill(CGRect(x: bx + bw * 0.62, y: 0.12, width: bw * 0.38, height: top - 0.12))
    // Steinfugen
    ctx.setStrokeColor(rgb(0.45, 0.38, 0.38, 0.30)); ctx.setLineWidth(0.003)
    for i in 0..<9 { let y = 0.16 + CGFloat(i) * 0.055; ctx.move(to: CGPoint(x: bx, y: y)); ctx.addLine(to: CGPoint(x: bx + bw, y: y)) }
    ctx.strokePath()
    ctx.restoreGState()
    // Parapet
    let px = bx - 0.03, pw = bw + 0.06
    ctx.saveGState(); ctx.addRect(CGRect(x: px, y: top - 0.012, width: pw, height: 0.062)); ctx.clip()
    grad([rgb(0.98, 0.92, 0.80), rgb(0.72, 0.66, 0.62)], CGPoint(x: px, y: 0.7), CGPoint(x: px + pw, y: 0.7))
    ctx.restoreGState()
    // Zinnen
    let n = 4, gap: CGFloat = 0.018
    let mw = (pw - gap * CGFloat(n - 1)) / CGFloat(n)
    for i in 0..<n {
        let x = px + CGFloat(i) * (mw + gap)
        ctx.saveGState(); ctx.addRect(CGRect(x: x, y: top + 0.045, width: mw, height: 0.062)); ctx.clip()
        grad([rgb(0.98, 0.92, 0.80), rgb(0.72, 0.66, 0.62)], CGPoint(x: x, y: 0.7), CGPoint(x: x + mw, y: 0.7))
        ctx.restoreGState()
    }
    // Window with warm light
    func arch(_ cx: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat, _ c: CGColor) {
        ctx.setFillColor(c)
        ctx.beginPath()
        ctx.move(to: CGPoint(x: cx - w / 2, y: y))
        ctx.addLine(to: CGPoint(x: cx - w / 2, y: y + h - w / 2))
        ctx.addArc(center: CGPoint(x: cx, y: y + h - w / 2), radius: w / 2, startAngle: .pi, endAngle: 0, clockwise: true)
        ctx.addLine(to: CGPoint(x: cx + w / 2, y: y))
        ctx.closePath(); ctx.fillPath()
    }
    arch(0.5, 0.45, 0.05, 0.10, rgb(1.0, 0.80, 0.32))
    arch(0.5, 0.12, 0.07, 0.12, rgb(0.20, 0.14, 0.24))   // Tor
    // Fahne
    ctx.setStrokeColor(rgb(0.25, 0.20, 0.30)); ctx.setLineWidth(0.006)
    ctx.move(to: CGPoint(x: 0.5, y: top + 0.107)); ctx.addLine(to: CGPoint(x: 0.5, y: 0.83)); ctx.strokePath()
    poly([(0.503, 0.83), (0.575, 0.805), (0.503, 0.78)], rgb(0.92, 0.28, 0.30))

    // Foreground hills
    poly([(0, 0.10), (0.20, 0.15), (0.40, 0.11), (0.60, 0.115), (0.80, 0.15), (1, 0.10), (1, 0), (0, 0)], rgb(0.08, 0.09, 0.20))
    // Glanz oben
    grad([rgb(1, 1, 1, 0.20), rgb(1, 1, 1, 0.0)], CGPoint(x: 0.5, y: 0.96), CGPoint(x: 0.5, y: 0.62))
    ctx.restoreGState()
    // feine Kante
    ctx.addPath(shape); ctx.setStrokeColor(rgb(1, 1, 1, 0.18)); ctx.setLineWidth(0.004); ctx.strokePath()
    return ctx.makeImage()!
}

func write(_ img: CGImage, _ path: String) {
    let d = CGImageDestinationCreateWithURL(URL(fileURLWithPath: path) as CFURL, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(d, img, nil)
    CGImageDestinationFinalize(d)
}

let out = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "AppIcon.iconset"
try? FileManager.default.createDirectory(atPath: out, withIntermediateDirectories: true)
for (name, px) in [("16x16", 16), ("16x16@2x", 32), ("32x32", 32), ("32x32@2x", 64), ("128x128", 128), ("128x128@2x", 256),
                   ("256x256", 256), ("256x256@2x", 512), ("512x512", 512), ("512x512@2x", 1024)] {
    write(draw(size: px), "\(out)/icon_\(name).png")
}
print("OK \(out)")
