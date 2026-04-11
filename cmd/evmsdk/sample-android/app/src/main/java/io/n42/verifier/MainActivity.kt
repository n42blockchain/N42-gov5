// SPDX-License-Identifier: LGPL-3.0-or-later
//
// Compose entry point for the N42 parallel verification mobile demo.
// Mirrors the layout of the iOS sample (cmd/evmsdk/sample-ios) so the
// two stay in feature parity.

package io.n42.verifier

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.collectAsState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

class MainActivity : ComponentActivity() {

    private val verifier = EvmsdkWrapper()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        verifier.initialize()
        setContent {
            MaterialTheme {
                VerifierScreen(verifier)
            }
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        verifier.free()
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun VerifierScreen(verifier: EvmsdkWrapper) {
    val status by verifier.status.collectAsState()
    val isConnected by verifier.isConnected.collectAsState()
    val blsKey by verifier.blsKeyHex.collectAsState()
    val stats by verifier.stats.collectAsState()
    val lastBlock by verifier.lastBlock.collectAsState()
    val log by verifier.verifyLog.collectAsState()
    val producers by verifier.producers.collectAsState()
    val policy by verifier.policy.collectAsState()

    var privKey by remember { mutableStateOf("") }
    var host by remember { mutableStateOf("127.0.0.1") }
    var port by remember { mutableStateOf("20013") }
    var account by remember { mutableStateOf("0x0000000000000000000000000000000000000000") }
    var addProducerURI by remember { mutableStateOf("ws://") }
    var addProducerAccount by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }

    Scaffold(topBar = { TopAppBar(title = { Text("N42 Verifier") }) }) { padding ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            item { StatusCard(status, isConnected, blsKey) }
            item {
                KeyCard(
                    privKey = privKey,
                    onPrivKeyChange = { privKey = it },
                    enabled = !isConnected,
                    onSet = {
                        try {
                            verifier.setPrivKey(privKey)
                            error = ""
                        } catch (e: Exception) {
                            error = e.message ?: "unknown"
                        }
                    },
                )
            }
            item {
                ServerCard(
                    host = host, onHostChange = { host = it },
                    port = port, onPortChange = { port = it },
                    account = account, onAccountChange = { account = it },
                    enabled = !isConnected,
                    canStart = !isConnected && blsKey.isNotEmpty(),
                    canStop = isConnected,
                    onStart = {
                        try {
                            verifier.setServer(host, port.toIntOrNull() ?: 20013, account)
                            verifier.start()
                            error = ""
                        } catch (e: Exception) {
                            error = e.message ?: "unknown"
                        }
                    },
                    onStop = { verifier.stop() },
                )
            }
            item {
                ProducersCard(
                    producers = producers,
                    addURI = addProducerURI, onAddURIChange = { addProducerURI = it },
                    addAccount = addProducerAccount, onAddAccountChange = { addProducerAccount = it },
                    onAdd = {
                        try {
                            verifier.addProducer(addProducerURI, addProducerAccount)
                            addProducerURI = "ws://"
                            addProducerAccount = ""
                            error = ""
                        } catch (e: Exception) {
                            error = e.message ?: "unknown"
                        }
                    },
                    onRemove = { uri ->
                        try {
                            verifier.removeProducer(uri)
                            error = ""
                        } catch (e: Exception) {
                            error = e.message ?: "unknown"
                        }
                    },
                )
            }
            item {
                PolicyCard(
                    policy = policy,
                    onApply = { p ->
                        try {
                            verifier.applyRotationPolicy(p)
                            error = ""
                        } catch (e: Exception) {
                            error = e.message ?: "unknown"
                        }
                    },
                )
            }
            item { StatsCard(stats) }
            lastBlock?.let { item { LatestBlockCard(it) } }
            if (error.isNotEmpty()) {
                item {
                    Card {
                        Text("Error: $error", color = Color.Red, modifier = Modifier.padding(12.dp))
                    }
                }
            }
            item { Text("Verification Log", style = MaterialTheme.typography.titleSmall) }
            items(log) { entry ->
                LogRow(entry)
            }
        }
    }
}

@Composable
private fun StatusCard(status: String, isConnected: Boolean, blsKey: String) {
    Card {
        Column(Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Surface(
                    modifier = Modifier.size(12.dp),
                    color = if (isConnected) Color.Green else Color.Gray,
                    shape = MaterialTheme.shapes.small,
                ) {}
                Spacer(Modifier.width(8.dp))
                Text("Status: $status")
            }
            if (blsKey.isNotEmpty()) {
                val short = if (blsKey.length > 16)
                    "${blsKey.substring(0, 8)}…${blsKey.substring(blsKey.length - 8)}"
                else blsKey
                Text(
                    "BLS Key: $short",
                    fontSize = 12.sp,
                    fontFamily = FontFamily.Monospace,
                    color = Color.Gray,
                )
            }
        }
    }
}

@Composable
private fun KeyCard(
    privKey: String,
    onPrivKeyChange: (String) -> Unit,
    enabled: Boolean,
    onSet: () -> Unit,
) {
    Card {
        Column(Modifier.padding(12.dp)) {
            Text("BLS Private Key (hex)", style = MaterialTheme.typography.titleSmall)
            OutlinedTextField(
                value = privKey,
                onValueChange = onPrivKeyChange,
                singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                modifier = Modifier.fillMaxWidth(),
                enabled = enabled,
            )
            Spacer(Modifier.height(8.dp))
            Button(onClick = onSet, enabled = enabled && privKey.isNotEmpty()) {
                Text("Set Key")
            }
        }
    }
}

@Composable
private fun ServerCard(
    host: String, onHostChange: (String) -> Unit,
    port: String, onPortChange: (String) -> Unit,
    account: String, onAccountChange: (String) -> Unit,
    enabled: Boolean,
    canStart: Boolean,
    canStop: Boolean,
    onStart: () -> Unit,
    onStop: () -> Unit,
) {
    Card {
        Column(Modifier.padding(12.dp)) {
            Text("Server", style = MaterialTheme.typography.titleSmall)
            OutlinedTextField(host, onHostChange, label = { Text("Host") },
                singleLine = true, enabled = enabled, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(port, onPortChange, label = { Text("Port") },
                singleLine = true, enabled = enabled, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(account, onAccountChange, label = { Text("Account") },
                singleLine = true, enabled = enabled, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(8.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = onStart, enabled = canStart, modifier = Modifier.weight(1f)) {
                    Text("Start")
                }
                OutlinedButton(onClick = onStop, enabled = canStop, modifier = Modifier.weight(1f)) {
                    Text("Stop")
                }
            }
        }
    }
}

@Composable
private fun ProducersCard(
    producers: List<EvmsdkWrapper.Producer>,
    addURI: String, onAddURIChange: (String) -> Unit,
    addAccount: String, onAddAccountChange: (String) -> Unit,
    onAdd: () -> Unit,
    onRemove: (String) -> Unit,
) {
    Card {
        Column(Modifier.padding(12.dp)) {
            Text("Producers (Multi-Node)", style = MaterialTheme.typography.titleSmall)
            if (producers.isEmpty()) {
                Text("No producers registered", fontSize = 12.sp, color = Color.Gray)
            } else {
                producers.forEach { p ->
                    Row(
                        modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Surface(
                            modifier = Modifier.size(if (p.active) 12.dp else 8.dp),
                            color = if (p.state == "started") Color.Green else Color.Gray,
                            shape = MaterialTheme.shapes.small,
                            border = if (p.active) BorderStroke(2.dp, Color.Blue) else null,
                        ) {}
                        Spacer(Modifier.width(8.dp))
                        Column(Modifier.weight(1f)) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text(p.uri, fontFamily = FontFamily.Monospace, fontSize = 12.sp)
                                if (p.active) {
                                    Spacer(Modifier.width(6.dp))
                                    Text("ACTIVE", color = Color.Blue,
                                        fontWeight = FontWeight.Bold, fontSize = 10.sp)
                                }
                            }
                            Text(p.account.take(10) + "…", fontSize = 10.sp, color = Color.Gray)
                        }
                        IconButton(onClick = { onRemove(p.uri) }) {
                            Text("✕", color = Color.Red)
                        }
                    }
                }
            }
            Spacer(Modifier.height(8.dp))
            OutlinedTextField(
                value = addURI, onValueChange = onAddURIChange,
                label = { Text("ws://host:port") },
                singleLine = true, modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = addAccount, onValueChange = onAddAccountChange,
                label = { Text("account 0x…") },
                singleLine = true, modifier = Modifier.fillMaxWidth(),
            )
            Spacer(Modifier.height(8.dp))
            Button(
                onClick = onAdd,
                enabled = addURI.length > 5 && addAccount.isNotEmpty(),
            ) {
                Text("Add Producer")
            }
        }
    }
}

@Composable
private fun PolicyCard(
    policy: EvmsdkWrapper.RotationPolicy,
    onApply: (EvmsdkWrapper.RotationPolicy) -> Unit,
) {
    var blockTime by remember(policy) { mutableStateOf(policy.blockTimeMs.toString()) }
    var slowTimeout by remember(policy) { mutableStateOf(policy.slowLeaderTimeoutMs.toString()) }
    var rotationCycle by remember(policy) { mutableStateOf(policy.rotationCycleMs.toString()) }

    Card {
        Column(Modifier.padding(12.dp)) {
            Text("Rotation Policy", style = MaterialTheme.typography.titleSmall)
            OutlinedTextField(
                value = blockTime, onValueChange = { blockTime = it },
                label = { Text("Block time (ms)") },
                singleLine = true, modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = slowTimeout, onValueChange = { slowTimeout = it },
                label = { Text("Slow leader timeout (ms)") },
                singleLine = true, modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = rotationCycle, onValueChange = { rotationCycle = it },
                label = { Text("Rotation cycle (ms)") },
                singleLine = true, modifier = Modifier.fillMaxWidth(),
            )
            Spacer(Modifier.height(8.dp))
            Button(onClick = {
                onApply(
                    EvmsdkWrapper.RotationPolicy(
                        blockTimeMs        = blockTime.toLongOrNull() ?: policy.blockTimeMs,
                        slowLeaderTimeoutMs = slowTimeout.toLongOrNull() ?: policy.slowLeaderTimeoutMs,
                        rotationCycleMs    = rotationCycle.toLongOrNull() ?: policy.rotationCycleMs,
                        monitorPeriodMs    = policy.monitorPeriodMs,
                    )
                )
            }) {
                Text("Apply Policy")
            }
            Text(
                "If active producer is silent for > slow timeout AND rotation cycle > slow timeout → switch. Otherwise wait.",
                fontSize = 10.sp, color = Color.Gray,
            )
        }
    }
}

@Composable
private fun StatsCard(stats: EvmsdkWrapper.Stats) {
    Card {
        Column(Modifier.padding(12.dp)) {
            Text("Stats", style = MaterialTheme.typography.titleSmall)
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceAround,
            ) {
                StatCell("Blocks", stats.blocksVerified.toString())
                StatCell("Success", "${stats.successRate}%")
                StatCell("Avg Time", "${stats.avgTimeMs}ms")
            }
            Text(
                "OK: ${stats.successCount}   Fail: ${stats.failureCount}",
                fontSize = 12.sp, color = Color.Gray,
            )
        }
    }
}

@Composable
private fun StatCell(title: String, value: String) {
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        Text(value, fontSize = 20.sp, fontWeight = FontWeight.Bold,
            fontFamily = FontFamily.Monospace)
        Text(title, fontSize = 12.sp, color = Color.Gray)
    }
}

@Composable
private fun LatestBlockCard(info: EvmsdkWrapper.BlockInfo) {
    Card {
        Column(Modifier.padding(12.dp)) {
            Text("Latest Block", style = MaterialTheme.typography.titleSmall)
            val short = if (info.blockHash.length > 18)
                "${info.blockHash.substring(0, 10)}…${info.blockHash.substring(info.blockHash.length - 6)}"
            else info.blockHash
            Text("#${info.blockNumber} $short",
                fontFamily = FontFamily.Monospace, fontSize = 14.sp)
            Text("Receipts root: ${info.computedReceiptsRoot.take(18)}…",
                fontSize = 12.sp, fontFamily = FontFamily.Monospace)
            Text("Txs: ${info.txCount} | ReadLog: ${info.witnessReadLogLen} | NewCodes: ${info.uncachedBytecodes}",
                fontSize = 12.sp)
            Text("Verify: ${info.verifyTimeMs}ms | Packet: ${"%.1f".format(info.packetSizeBytes / 1024.0)}KB",
                fontSize = 12.sp, color = Color.Gray)
        }
    }
}

@Composable
private fun LogRow(entry: EvmsdkWrapper.LogEntry) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text("#${entry.blockNumber}", fontFamily = FontFamily.Monospace, fontSize = 12.sp)
        Spacer(Modifier.weight(1f))
        Text(if (entry.success) "✓" else "✗",
            color = if (entry.success) Color.Green else Color.Red,
            fontSize = 14.sp)
        Spacer(Modifier.width(8.dp))
        Text("${entry.verifyTimeMs}ms",
            fontFamily = FontFamily.Monospace, fontSize = 12.sp, color = Color.Gray)
    }
}

@Preview(showBackground = true)
@Composable
fun VerifierScreenPreview() {
    MaterialTheme { VerifierScreen(EvmsdkWrapper()) }
}
