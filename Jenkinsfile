pipeline {
	agent {
		label "operator-builder"
	}

	stages {
		stage("Build Operator") {
			steps {
				build(job: "${CBT_OPERATOR_BUILD_JOB_NAME}", parameters: [string(name: "GIT_BRANCH", value: GIT_BRANCH)])
			}
		}
		stage ('Run Operator Test') {
			steps {
				build(job: "${CBT_OPERATOR_TEST_JOB_NAME}", parameters: [string(name: "GIT_BRANCH", value: GIT_COMMIT)])
			}
		}
	}
}

