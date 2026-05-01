// AttendFriction — minimal SwiftUI friction screen.
//
// Spawned by attendd when a friction rule fires. Reads its configuration from
// CLI args (or stdin JSON), presents the challenge, and POSTs the outcome
// back to the daemon at http://127.0.0.1:7723/v1/friction/result.
//
// Usage:
//   AttendFriction --level intent --target reddit.com [--challenge-id <id>]
//   AttendFriction --level timer  --target Slack --timer-seconds 30
//
// Build:
//   swiftc -O -framework SwiftUI -framework AppKit \
//       swift/AttendFriction/main.swift \
//       -o ~/.local/bin/AttendFriction

import SwiftUI
import AppKit
import Foundation

// MARK: - Config

struct FrictionConfig {
    enum Level: String { case timer, intent, phrase, math, breath }

    let level: Level
    let target: String
    let timerSeconds: Int
    let phrase: String
    let challengeID: String
    let daemonURL: String

    static func parse(_ argv: [String]) -> FrictionConfig {
        var args: [String: String] = [:]
        var i = 1
        while i < argv.count {
            let key = argv[i]
            guard key.hasPrefix("--"), i + 1 < argv.count else { i += 1; continue }
            args[key] = argv[i + 1]
            i += 2
        }
        let levelStr = args["--level"] ?? "intent"
        return FrictionConfig(
            level: Level(rawValue: levelStr) ?? .intent,
            target: args["--target"] ?? "(unknown)",
            timerSeconds: Int(args["--timer-seconds"] ?? "10") ?? 10,
            phrase: args["--phrase"] ?? "",
            challengeID: args["--challenge-id"] ?? "",
            daemonURL: args["--daemon-url"] ?? "http://127.0.0.1:7723"
        )
    }
}

// MARK: - Result reporting

func reportResult(_ cfg: FrictionConfig, passed: Bool, intent: String?) {
    guard let url = URL(string: "\(cfg.daemonURL)/v1/friction/result") else { return }
    var req = URLRequest(url: url)
    req.httpMethod = "POST"
    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    let body: [String: Any] = [
        "challenge_id": cfg.challengeID,
        "target": cfg.target,
        "passed": passed,
        "intent": intent ?? "",
    ]
    req.httpBody = try? JSONSerialization.data(withJSONObject: body)

    let sem = DispatchSemaphore(value: 0)
    URLSession.shared.dataTask(with: req) { _, _, _ in sem.signal() }.resume()
    _ = sem.wait(timeout: .now() + .seconds(2))
}

// MARK: - Views

struct ChallengeView: View {
    let cfg: FrictionConfig
    let onClose: (Bool) -> Void

    var body: some View {
        ZStack {
            Color.black.opacity(0.92).ignoresSafeArea()
            VStack(spacing: 24) {
                Text("attend").font(.system(size: 14, weight: .medium)).foregroundColor(.gray)
                Text(cfg.target).font(.system(size: 32, weight: .semibold)).foregroundColor(.white)

                switch cfg.level {
                case .timer:
                    TimerChallenge(seconds: cfg.timerSeconds) { onClose(true) }
                case .intent:
                    IntentChallenge(target: cfg.target) { intent in
                        // Caller-of-Window also reports intent; for v1 we just pass.
                        _ = intent
                        onClose(true)
                    }
                case .phrase:
                    PhraseChallenge(phrase: cfg.phrase) { onClose(true) }
                case .math:
                    MathChallenge { onClose(true) }
                case .breath:
                    TimerChallenge(seconds: cfg.timerSeconds) { onClose(true) } // placeholder
                }

                Button("cancel — go back") { onClose(false) }
                    .keyboardShortcut(.escape)
                    .buttonStyle(.plain)
                    .foregroundColor(.gray)
            }
            .padding(48)
            .frame(maxWidth: 600)
        }
    }
}

struct TimerChallenge: View {
    let seconds: Int
    let onPass: () -> Void
    @State private var remaining: Int

    init(seconds: Int, onPass: @escaping () -> Void) {
        self.seconds = seconds
        self.onPass = onPass
        _remaining = State(initialValue: seconds)
    }

    var body: some View {
        VStack(spacing: 16) {
            Text("\(remaining)").font(.system(size: 96, weight: .bold)).foregroundColor(.white)
            Text("Wait, then proceed.").foregroundColor(.gray)
        }
        .onAppear {
            Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { t in
                remaining -= 1
                if remaining <= 0 { t.invalidate(); onPass() }
            }
        }
    }
}

struct IntentChallenge: View {
    let target: String
    let onPass: (String) -> Void
    @State private var text: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Why are you opening \(target)?").foregroundColor(.white)
            TextEditor(text: $text)
                .frame(height: 120)
                .padding(8)
                .background(Color.white.opacity(0.05))
                .foregroundColor(.white)
            HStack {
                Spacer()
                Button("Proceed") { onPass(text) }
                    .disabled(text.trimmingCharacters(in: .whitespaces).count < 8)
            }
        }
    }
}

struct PhraseChallenge: View {
    let phrase: String
    let onPass: () -> Void
    @State private var typed: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Type this phrase to proceed:").foregroundColor(.gray)
            Text(phrase).font(.system(.body, design: .monospaced)).foregroundColor(.white)
            TextField("type here", text: $typed)
                .textFieldStyle(.roundedBorder)
            HStack {
                Spacer()
                Button("Proceed") { onPass() }.disabled(typed != phrase)
            }
        }
    }
}

struct MathChallenge: View {
    let onPass: () -> Void
    @State private var a: Int = Int.random(in: 12...49)
    @State private var b: Int = Int.random(in: 12...49)
    @State private var typed: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("\(a) × \(b) = ?").foregroundColor(.white).font(.title)
            TextField("answer", text: $typed)
                .textFieldStyle(.roundedBorder)
                .frame(width: 200)
            HStack {
                Spacer()
                Button("Proceed") { onPass() }
                    .disabled(Int(typed) != a * b)
            }
        }
    }
}

// MARK: - App

let cfg = FrictionConfig.parse(CommandLine.arguments)

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate

class AppDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!

    func applicationDidFinishLaunching(_ notification: Notification) {
        let view = ChallengeView(cfg: cfg) { passed in
            reportResult(cfg, passed: passed, intent: nil)
            NSApp.terminate(nil)
        }
        window = NSWindow(
            contentRect: NSScreen.main?.frame ?? .zero,
            styleMask: [.borderless],
            backing: .buffered, defer: false)
        window.level = .screenSaver
        window.isOpaque = false
        window.backgroundColor = .clear
        window.contentView = NSHostingView(rootView: view)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }
}

app.setActivationPolicy(.regular)
app.run()
