// SPDX-License-Identifier: LGPL-3.0-or-later
//
// MinimalView — the "minimal node" screen: a zero-trust light client that follows
// the chain tip with ~1 day of rolling data and verifies account balances against
// the header-chain-trusted state root. Dark, monospace-numeric, trust-flow themed.

import SwiftUI

struct MinimalView: View {
    @StateObject private var node = MinimalWrapper()

    // Demo IDC + a recent social checkpoint (replace with finalized values).
    @State private var idcURL = "http://127.0.0.1:8555"
    @State private var cpBlock = "990000"
    @State private var cpHash  = "0x31568223eb0aff7ed01b2ef77d3c61b051ef00781032e00537a7a17176d5e67d"
    @State private var addr    = "0xdAC17F958D2ee523a2206206994597C13D831ec7"
    @State private var pulse   = false

    private let bg     = Color(red: 0.04, green: 0.05, blue: 0.07)
    private let green  = Color(red: 0.20, green: 0.90, blue: 0.55)
    private let dim    = Color.white.opacity(0.45)

    var body: some View {
        ZStack {
            bg.ignoresSafeArea()
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    header
                    chainStrip
                    syncCard
                    accountsCard
                    resourceFooter
                }
                .padding(20)
            }
        }
        .preferredColorScheme(.dark)
        .onAppear { pulse = true }
    }

    // MARK: Header — title + zero-trust badge + head
    private var header: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 2) {
                Text("N42 · MINIMAL").font(.system(size: 22, weight: .heavy, design: .rounded))
                Text(node.status).font(.system(size: 12, design: .monospaced)).foregroundColor(dim)
            }
            Spacer()
            HStack(spacing: 6) {
                Image(systemName: "checkmark.shield.fill")
                Text("ZERO-TRUST").font(.system(size: 11, weight: .bold))
            }
            .foregroundColor(green)
            .padding(.horizontal, 10).padding(.vertical, 6)
            .background(green.opacity(0.12)).clipShape(Capsule())
        }
    }

    // MARK: Chain strip — flowing blocks + trust-root anchor
    private var chainStrip: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Label("HEAD \(node.head)", systemImage: "cube.fill")
                    .font(.system(size: 13, weight: .semibold, design: .monospaced)).foregroundColor(green)
                Spacer()
                Text(short(node.headHash)).font(.system(size: 11, design: .monospaced)).foregroundColor(dim)
            }
            HStack(spacing: 4) {
                Image(systemName: "anchor").foregroundColor(.cyan).font(.system(size: 12))
                ForEach(0..<22, id: \.self) { i in
                    RoundedRectangle(cornerRadius: 2)
                        .fill(i < 18 ? green.opacity(node.syncing ? (pulse ? 0.9 : 0.5) : 0.8) : Color.white.opacity(0.10))
                        .frame(width: 11, height: 22)
                        .animation(.easeInOut(duration: 0.7).repeatForever().delay(Double(i) * 0.03), value: pulse)
                }
                Image(systemName: "arrow.right").foregroundColor(dim).font(.system(size: 11))
            }
            HStack {
                Text("⚓ trust root \(node.trustBlock)").font(.system(size: 11, design: .monospaced)).foregroundColor(.cyan)
                Spacer()
                Text("retained \(node.retained) blk · re-anchors \(node.reanchors)")
                    .font(.system(size: 11, design: .monospaced)).foregroundColor(dim)
            }
            if node.syncing {
                ProgressView(value: node.syncProgress).tint(green)
            }
        }
        .padding(16).background(Color.white.opacity(0.04)).cornerRadius(16)
    }

    // MARK: Sync card — connect + follow
    private var syncCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            field("IDC URL", $idcURL)
            HStack(spacing: 10) {
                field("checkpoint #", $cpBlock).frame(maxWidth: 130)
                field("checkpoint hash", $cpHash)
            }
            HStack(spacing: 12) {
                Button(node.initialized ? "Re-anchor" : "Connect") {
                    node.initialize(idcURL: idcURL, checkpointBlock: Int64(cpBlock) ?? 0, checkpointHash: cpHash)
                }
                .buttonStyle(Filled(color: .cyan))

                Button(node.following ? "● Live (12s)" : "Follow tip ▶") {
                    if node.following { node.stopFollowing() } else { node.startFollowing() }
                }
                .buttonStyle(Filled(color: node.following ? .red : green))
                .disabled(!node.initialized)

                Button("Replay ② @head") { node.verifyBlock(node.head) }
                    .buttonStyle(Filled(color: .orange))
                    .disabled(!node.initialized || node.head == 0)
            }
            if !node.lastVerify.isEmpty {
                Text(node.lastVerify).font(.system(size: 12, design: .monospaced)).foregroundColor(.orange)
            }
        }
        .padding(16).background(Color.white.opacity(0.04)).cornerRadius(16)
    }

    // MARK: Accounts — trustless balances
    private var accountsCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                field("account 0x…", $addr)
                Button {
                    node.verifyBalance(addr)
                } label: { Image(systemName: "magnifyingglass.circle.fill").font(.system(size: 30)) }
                    .foregroundColor(green).disabled(!node.initialized)
            }
            ForEach(node.accounts) { a in accountRow(a) }
        }
        .padding(16).background(Color.white.opacity(0.04)).cornerRadius(16)
    }

    private func accountRow(_ a: MinimalWrapper.Account) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 3) {
                Text(short(a.address)).font(.system(size: 13, weight: .semibold, design: .monospaced))
                Text(a.exists ? "\(weiToEth(a.balanceWei)) ETH · nonce \(a.nonce)" : "absent @ \(a.block)")
                    .font(.system(size: 12, design: .monospaced)).foregroundColor(dim)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 3) {
                Image(systemName: a.verified ? "checkmark.shield.fill" : "xmark.shield")
                    .foregroundColor(a.verified ? green : .red)
                Text("\(a.proofBytes) B").font(.system(size: 10, design: .monospaced)).foregroundColor(dim)
            }
        }
        .padding(12).background(Color.black.opacity(0.25)).cornerRadius(12)
    }

    // MARK: Resource footer
    private var resourceFooter: some View {
        HStack(spacing: 18) {
            gauge("RAM", "~80 MB", green)
            gauge("SSD", "~1 day", .cyan)
            gauge("③ proof", node.lastProofBytes > 0 ? "\(node.lastProofBytes) B" : "—", .orange)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 10)
    }

    private func gauge(_ k: String, _ v: String, _ c: Color) -> some View {
        VStack(spacing: 3) {
            Text(v).font(.system(size: 14, weight: .bold, design: .monospaced)).foregroundColor(c)
            Text(k).font(.system(size: 10)).foregroundColor(dim)
        }
    }

    // MARK: bits
    private func field(_ ph: String, _ b: Binding<String>) -> some View {
        TextField(ph, text: b)
            .font(.system(size: 12, design: .monospaced))
            .padding(10).background(Color.black.opacity(0.3)).cornerRadius(10)
            .autocorrectionDisabled().textInputAutocapitalization(.never)
    }
    private func short(_ s: String) -> String { s.count > 14 ? "\(s.prefix(8))…\(s.suffix(4))" : s }
    private func weiToEth(_ wei: String) -> String {
        guard let d = Double(wei) else { return wei }
        return String(format: "%.4f", d / 1e18)
    }

    struct Filled: ButtonStyle {
        let color: Color
        func makeBody(configuration: Configuration) -> some View {
            configuration.label
                .font(.system(size: 14, weight: .bold))
                .padding(.horizontal, 16).padding(.vertical, 10)
                .background(color.opacity(configuration.isPressed ? 0.5 : 0.18))
                .foregroundColor(color).cornerRadius(12)
        }
    }
}

#Preview { MinimalView() }
