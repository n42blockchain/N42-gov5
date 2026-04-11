// SPDX-License-Identifier: LGPL-3.0-or-later
//
// SwiftUI view that drives the parallel-verification mobile track via
// EvmsdkWrapper. Mirrors the layout of D:\N42\n42-26\mobile\ios\N42Verifier
// but talks to the gomobile-bound Go SDK instead of the Rust C FFI.

import SwiftUI

struct ContentView: View {
    @StateObject private var verifier = EvmsdkWrapper()
    @State private var serverHost = "127.0.0.1"
    @State private var serverPort = "20013"
    @State private var account = "0x0000000000000000000000000000000000000000"
    @State private var privKeyHex = ""
    @State private var lastError = ""
    @State private var addProducerURI = "ws://"
    @State private var addProducerAccount = ""

    var body: some View {
        NavigationView {
            List {
                statusSection
                keySection
                connectionSection
                producersSection
                policySection
                statsSection
                if verifier.lastBlockInfo != nil {
                    latestBlockSection
                }
                logSection
                if !lastError.isEmpty {
                    Section("Error") {
                        Text(lastError).foregroundColor(.red).font(.caption)
                    }
                }
            }
            .navigationTitle("N42 Verifier")
            .onAppear { verifier.initialize() }
        }
    }

    // MARK: Status

    private var statusSection: some View {
        Section {
            HStack {
                Circle()
                    .fill(verifier.isConnected ? Color.green : Color.gray)
                    .frame(width: 12, height: 12)
                Text("Status: \(verifier.statusText)")
            }
            if !verifier.blsKeyHex.isEmpty {
                let key = verifier.blsKeyHex
                Text("BLS Key: \(key.prefix(8))…\(key.suffix(8))")
                    .font(.caption)
                    .monospaced()
                    .foregroundColor(.secondary)
            }
        }
    }

    // MARK: Key

    private var keySection: some View {
        Section("BLS Private Key (hex)") {
            SecureField("64-hex-char private key", text: $privKeyHex)
                .disableAutocorrection(true)
                .autocapitalization(.none)
            Button("Set Key") {
                do {
                    try verifier.setPrivKey(privKeyHex)
                    lastError = ""
                } catch {
                    lastError = error.localizedDescription
                }
            }
            .disabled(privKeyHex.isEmpty || verifier.isConnected)
        }
    }

    // MARK: Connection

    private var connectionSection: some View {
        Section("Server") {
            TextField("Host", text: $serverHost)
                .disabled(verifier.isConnected)
                .autocapitalization(.none)
                .disableAutocorrection(true)
            TextField("Port", text: $serverPort)
                .disabled(verifier.isConnected)
                .keyboardType(.numberPad)
            TextField("Account", text: $account)
                .disabled(verifier.isConnected)
                .autocapitalization(.none)
                .disableAutocorrection(true)
            HStack(spacing: 12) {
                Button("Start") {
                    let port = UInt16(serverPort) ?? 20013
                    do {
                        try verifier.setServer(host: serverHost, port: port, account: account)
                        try verifier.start()
                        lastError = ""
                    } catch {
                        lastError = error.localizedDescription
                    }
                }
                .frame(maxWidth: .infinity)
                .disabled(verifier.isConnected || verifier.blsKeyHex.isEmpty)
                .buttonStyle(.borderedProminent)

                Button("Stop") { verifier.stop() }
                    .frame(maxWidth: .infinity)
                    .disabled(!verifier.isConnected)
                    .buttonStyle(.bordered)
            }
        }
    }

    // MARK: Stats

    private var statsSection: some View {
        Section("Stats") {
            HStack {
                StatCard(title: "Blocks", value: "\(verifier.blocksVerified)")
                Spacer()
                StatCard(title: "Success", value: "\(verifier.successRate)%")
                Spacer()
                StatCard(title: "Avg Time", value: "\(verifier.avgTimeMs)ms")
            }
            Text("OK: \(verifier.successCount)   Fail: \(verifier.failureCount)")
                .font(.caption)
                .foregroundColor(.secondary)
        }
    }

    // MARK: Latest block

    private var latestBlockSection: some View {
        Section("Latest Block") {
            if let info = verifier.lastBlockInfo {
                VStack(alignment: .leading, spacing: 4) {
                    Text("#\(info.blockNumber) \(info.blockHash.prefix(10))…\(info.blockHash.suffix(6))")
                        .monospaced()
                        .font(.subheadline)
                    Text("Receipts root: \(info.computedReceiptsRoot.prefix(18))…")
                        .font(.caption)
                        .monospaced()
                    Text("Txs: \(info.txCount) | ReadLog: \(info.witnessReadLogLen) | NewCodes: \(info.uncachedBytecodes)")
                        .font(.caption)
                    Text("Verify: \(info.verifyTimeMs)ms | Packet: \(String(format: "%.1f", Double(info.packetSizeBytes) / 1024.0))KB")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            }
        }
    }

    // MARK: Log

    private var logSection: some View {
        Section("Verification Log") {
            ForEach(verifier.verifyLog) { entry in
                HStack {
                    Text("#\(entry.blockNumber)")
                        .monospaced()
                        .font(.caption)
                    Spacer()
                    Image(systemName: entry.success ? "checkmark.circle.fill" : "xmark.circle.fill")
                        .foregroundColor(entry.success ? .green : .red)
                        .font(.caption)
                    Text("\(entry.verifyTimeMs)ms")
                        .monospaced()
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            }
        }
    }

    // MARK: Producers

    private var producersSection: some View {
        Section("Producers (Multi-Node)") {
            if verifier.producers.isEmpty {
                Text("No producers registered")
                    .font(.caption)
                    .foregroundColor(.secondary)
            } else {
                ForEach(verifier.producers) { p in
                    HStack {
                        // Green = engine running. Blue ring = active candidate.
                        ZStack {
                            if p.active {
                                Circle()
                                    .stroke(Color.blue, lineWidth: 2)
                                    .frame(width: 14, height: 14)
                            }
                            Circle()
                                .fill(p.state == "started" ? Color.green : Color.gray)
                                .frame(width: 8, height: 8)
                        }
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(spacing: 4) {
                                Text(p.uri).font(.caption).monospaced()
                                if p.active {
                                    Text("ACTIVE")
                                        .font(.caption2)
                                        .fontWeight(.bold)
                                        .foregroundColor(.blue)
                                }
                            }
                            Text(p.account.prefix(10) + "…")
                                .font(.caption2)
                                .foregroundColor(.secondary)
                        }
                        Spacer()
                        Button(role: .destructive) {
                            do {
                                try verifier.removeProducer(uri: p.uri)
                                lastError = ""
                            } catch {
                                lastError = error.localizedDescription
                            }
                        } label: {
                            Image(systemName: "minus.circle")
                        }
                        .buttonStyle(.borderless)
                    }
                }
            }
            // Add new producer
            VStack(alignment: .leading, spacing: 4) {
                TextField("ws://host:port", text: $addProducerURI)
                    .autocapitalization(.none)
                    .disableAutocorrection(true)
                TextField("account 0x…", text: $addProducerAccount)
                    .autocapitalization(.none)
                    .disableAutocorrection(true)
                Button("Add Producer") {
                    do {
                        try verifier.addProducer(uri: addProducerURI, account: addProducerAccount)
                        addProducerURI = "ws://"
                        addProducerAccount = ""
                        lastError = ""
                    } catch {
                        lastError = error.localizedDescription
                    }
                }
                .disabled(addProducerURI.count <= 5 || addProducerAccount.isEmpty)
            }
        }
    }

    // MARK: Rotation Policy

    private var policySection: some View {
        Section("Rotation Policy") {
            HStack {
                Text("Block time").font(.caption)
                Spacer()
                TextField("ms", value: $verifier.rotationPolicy.blockTimeMs, formatter: NumberFormatter())
                    .frame(width: 80)
                    .multilineTextAlignment(.trailing)
                    .keyboardType(.numberPad)
            }
            HStack {
                Text("Slow leader timeout").font(.caption)
                Spacer()
                TextField("ms", value: $verifier.rotationPolicy.slowLeaderTimeoutMs, formatter: NumberFormatter())
                    .frame(width: 80)
                    .multilineTextAlignment(.trailing)
                    .keyboardType(.numberPad)
            }
            HStack {
                Text("Rotation cycle").font(.caption)
                Spacer()
                TextField("ms", value: $verifier.rotationPolicy.rotationCycleMs, formatter: NumberFormatter())
                    .frame(width: 80)
                    .multilineTextAlignment(.trailing)
                    .keyboardType(.numberPad)
            }
            Button("Apply Policy") {
                do {
                    try verifier.applyRotationPolicy()
                    lastError = ""
                } catch {
                    lastError = error.localizedDescription
                }
            }
            // Hint about the algorithm
            Text("If active producer is silent for > slow timeout AND rotation cycle > slow timeout → switch to next candidate. Otherwise wait.")
                .font(.caption2)
                .foregroundColor(.secondary)
        }
    }
}

struct StatCard: View {
    let title: String
    let value: String

    var body: some View {
        VStack {
            Text(value)
                .font(.title2)
                .monospaced()
                .fontWeight(.bold)
            Text(title)
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .frame(minWidth: 80)
    }
}

struct ContentView_Previews: PreviewProvider {
    static var previews: some View { ContentView() }
}
